package sipgo

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialogClientValidatesChallengeBeforeCredentials(t *testing.T) {
	for _, test := range []struct {
		name            string
		status          int
		challengeHeader string
		authHeader      string
	}{
		{name: "origin", status: sip.StatusUnauthorized, challengeHeader: "WWW-Authenticate", authHeader: "Authorization"},
		{name: "proxy", status: sip.StatusProxyAuthRequired, challengeHeader: "Proxy-Authenticate", authHeader: "Proxy-Authorization"},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			client := testClient(t, func(request *sip.Request) *sip.Response {
				attempts++
				if attempts == 2 {
					require.NotNil(t, request.GetHeader(test.authHeader))
					return sip.NewResponseFromRequest(request, sip.StatusOK, "OK", nil)
				}
				response := sip.NewResponseFromRequest(request, test.status, "Authentication Required", nil)
				response.AppendHeader(sip.NewHeader(
					test.challengeHeader,
					`Digest realm="expected", nonce="first", algorithm=MD5`,
				))
				return response
			})

			dialog, err := (&DialogUA{Client: client}).Invite(
				context.Background(), sip.Uri{User: "callee", Host: "example.test"}, nil,
			)
			require.NoError(t, err)
			validationCalls := 0
			err = dialog.WaitAnswer(context.Background(), AnswerOptions{
				Username: "alice", Password: "secret",
				ValidateChallenge: func(response *sip.Response) error {
					validationCalls++
					require.Nil(t, dialog.InviteRequest.GetHeader(test.authHeader))
					require.NotNil(t, response.GetHeader(test.challengeHeader))
					response.RemoveHeader(test.challengeHeader)
					return nil
				},
			})
			require.NoError(t, err)
			assert.Equal(t, 1, validationCalls)
			assert.Equal(t, 2, attempts)
		})
	}
}

func TestDialogClientChallengeRejectionSendsNoCredentials(t *testing.T) {
	rejected := errors.New("challenge rejected")
	attempts := 0
	client := testClient(t, func(request *sip.Request) *sip.Response {
		attempts++
		response := sip.NewResponseFromRequest(request, sip.StatusUnauthorized, "Unauthorized", nil)
		response.AppendHeader(sip.NewHeader(
			"WWW-Authenticate", `Digest realm="wrong", nonce="first", algorithm=MD5`,
		))
		return response
	})
	dialog, err := (&DialogUA{Client: client}).Invite(
		context.Background(), sip.Uri{User: "callee", Host: "example.test"}, nil,
	)
	require.NoError(t, err)

	err = dialog.WaitAnswer(context.Background(), AnswerOptions{
		Username: "alice", Password: "secret",
		ValidateChallenge: func(response *sip.Response) error {
			require.NotNil(t, response.GetHeader("WWW-Authenticate"))
			return rejected
		},
		OnResponse: func(*sip.Response) error {
			t.Fatal("rejected challenge reached response callback")
			return nil
		},
	})
	require.ErrorIs(t, err, rejected)
	assert.Equal(t, 1, attempts)
	assert.Nil(t, dialog.InviteRequest.GetHeader("Authorization"))
}

func TestDialogClientValidatesEveryRechallenge(t *testing.T) {
	attempts := 0
	client := testClient(t, func(request *sip.Request) *sip.Response {
		attempts++
		if attempts == 3 {
			return sip.NewResponseFromRequest(request, sip.StatusOK, "OK", nil)
		}
		response := sip.NewResponseFromRequest(request, sip.StatusUnauthorized, "Unauthorized", nil)
		response.AppendHeader(sip.NewHeader(
			"WWW-Authenticate",
			`Digest realm="expected", nonce="nonce-`+strconv.Itoa(attempts)+`", algorithm=MD5`,
		))
		return response
	})
	dialog, err := (&DialogUA{Client: client}).Invite(
		context.Background(), sip.Uri{User: "callee", Host: "example.test"}, nil,
	)
	require.NoError(t, err)

	validationCalls := 0
	err = dialog.WaitAnswer(context.Background(), AnswerOptions{
		Username: "alice", Password: "secret",
		ValidateChallenge: func(*sip.Response) error {
			validationCalls++
			return nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, validationCalls)
	assert.Equal(t, 3, attempts)
	authorization := dialog.InviteRequest.GetHeaders("Authorization")
	require.Len(t, authorization, 1)
	assert.Contains(t, authorization[0].Value(), `nonce="nonce-2"`)
}
