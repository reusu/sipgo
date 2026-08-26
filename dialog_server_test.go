package sipgo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/emiago/sipgo/fakes"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialogServerByeRequest(t *testing.T) {
	ua, _ := NewUA()
	defer ua.Close()
	cli, _ := NewClient(ua)

	uasContact := sip.ContactHeader{
		Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
	}
	dialogSrv := NewDialogServerCache(cli, uasContact)

	invite, _, _ := createTestInvite(t, "sip:uas@uas.com", "udp", "uas.com:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})
	invite.AppendHeader(&sip.RecordRouteHeader{Address: sip.Uri{Host: "P1", Port: 5060}})
	invite.AppendHeader(&sip.RecordRouteHeader{Address: sip.Uri{Host: "P2", Port: 5060}})
	invite.AppendHeader(&sip.RecordRouteHeader{Address: sip.Uri{Host: "P3", Port: 5060}})

	dialog, err := dialogSrv.ReadInvite(invite, sip.NewServerTx("test", invite, nil, slog.Default()))
	require.NoError(t, err)

	res := sip.NewResponseFromRequest(invite, sip.StatusOK, "OK", nil)
	res.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uac", Port: 9876}})

	bye := sip.NewRequest(sip.BYE, invite.Contact().Address)
	ctxCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	// No execution
	dialog.TransactionRequest(ctxCanceled, bye)
	require.Equal(t, invite.CallID(), bye.CallID())

	routes := bye.GetHeaders("Route")
	assert.Equal(t, "<sip:P1:5060>", routes[0].Value())
	assert.Equal(t, "<sip:P2:5060>", routes[1].Value())
	assert.Equal(t, "<sip:P3:5060>", routes[2].Value())
}

func TestDialogServerTransactionCanceled(t *testing.T) {
	// sip.Timer_H = 0

	ua, _ := NewUA()
	defer ua.Close()
	cli, _ := NewClient(ua)

	uasContact := sip.ContactHeader{
		Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
	}
	dialogSrv := NewDialogServerCache(cli, uasContact)

	invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})

	t.Run("TerminatedEarly", func(t *testing.T) {
		tx := sip.NewServerTx("test", invite, nil, slog.Default())
		tx.Terminate()
		_, err := dialogSrv.ReadInvite(invite, tx)
		require.Error(t, err)
		require.ErrorIs(t, err, sip.ErrTransactionTerminated)
	})

	t.Run("TerminatedByCancel", func(t *testing.T) {
		conn := &sip.UDPConnection{
			PacketConn: &fakes.UDPConn{
				Writers: map[string]io.Writer{
					"127.0.0.1:5090": bytes.NewBuffer(make([]byte, 0)),
				},
			},
		}
		tx := sip.NewServerTx("test", invite, conn, slog.Default())
		tx.Init()
		d, err := dialogSrv.ReadInvite(invite, tx)
		require.NoError(t, err)

		err = tx.Receive(newCancelRequest(invite))
		require.NoError(t, err)
		// Context dialog will be terminated and in this case
		// cause of context cancelation could be found
		<-d.Context().Done()
		require.ErrorIs(t, d.err(), sip.ErrTransactionCanceled)
		res200 := sip.NewResponseFromRequest(d.InviteRequest, 200, "OK", nil)
		err = d.WriteResponseContext(context.Background(), res200)
		require.ErrorIs(t, err, sip.ErrTransactionCanceled)
	})

	t.Run("TerminatedByCancelBeforeReadingInvite", func(t *testing.T) {
		conn := &sip.UDPConnection{
			PacketConn: &fakes.UDPConn{
				Writers: map[string]io.Writer{
					"127.0.0.1:5090": bytes.NewBuffer(make([]byte, 0)),
				},
			},
		}
		tx := sip.NewServerTx("test", invite, conn, slog.Default())
		tx.Init()
		err := tx.Receive(newCancelRequest(invite))
		require.NoError(t, err)
		_, err = dialogSrv.ReadInvite(invite, tx)
		require.ErrorIs(t, err, sip.ErrTransactionCanceled)
	})

}

func TestDialogServerRequestsWithinDialog(t *testing.T) {
	// https://datatracker.ietf.org/doc/html/rfc3261#section-12.2.2

	ua, _ := NewUA()
	defer ua.Close()
	cli, _ := NewClient(ua)

	uasContact := sip.ContactHeader{
		Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
	}
	dialogSrv := NewDialogServerCache(cli, uasContact)

	invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})

	t.Run("InvalidCseq", func(t *testing.T) {
		// This covers issue explained as
		// https://github.com/emiago/sipgo/issues/187
		conn := &sip.UDPConnection{
			PacketConn: &fakes.UDPConn{
				Writers: map[string]io.Writer{
					"127.0.0.1:5090": bytes.NewBuffer(make([]byte, 0)),
				},
			},
		}
		tx := sip.NewServerTx("test", invite, conn, slog.Default())
		tx.Init()

		dialog, err := dialogSrv.ReadInvite(invite, tx)
		require.NoError(t, err)
		defer dialog.Close()

		byeWrongCseq := newByeRequestUAC(invite, sip.NewResponseFromRequest(invite, 200, "OK", nil), nil)
		byeWrongCseq.CSeq().SeqNo--
		tx = sip.NewServerTx("test", byeWrongCseq, conn, slog.Default())
		tx.Init()
		err = dialog.ReadBye(byeWrongCseq, tx)
		require.ErrorIs(t, err, ErrDialogInvalidCseq)
	})

	t.Run("TerminateAfterSentRequest", func(t *testing.T) {
		// This covers issue explained as
		// https://github.com/emiago/sipgo/issues/187
		conn := &sip.UDPConnection{
			PacketConn: &fakes.UDPConn{
				Writers: map[string]io.Writer{
					"127.0.0.1:5090": bytes.NewBuffer(make([]byte, 0)),
				},
			},
		}
		tx := sip.NewServerTx("test", invite, conn, slog.Default())
		tx.Init()

		dialog, err := dialogSrv.ReadInvite(invite, tx)
		require.NoError(t, err)
		defer dialog.Close()

		reinvite := sip.NewRequest(sip.INVITE, invite.From().Address)
		_, err = dialog.TransactionRequest(context.TODO(), reinvite)
		require.NoError(t, err)

		bye := newByeRequestUAC(invite, sip.NewResponseFromRequest(invite, 200, "OK", nil), nil)
		tx = sip.NewServerTx("test-bye", bye, conn, slog.Default())
		tx.Init()
		err = dialog.ReadBye(bye, tx)
		require.NoError(t, err)
	})
}

func TestDialogServer2xxRetransmission(t *testing.T) {
	// sip.T1 = 1
	ua, _ := NewUA()
	defer ua.Close()
	cli, _ := NewClient(ua)

	uasContact := sip.ContactHeader{
		Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
	}
	dialogSrv := NewDialogServerCache(cli, uasContact)

	invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})

	// Create a server transcation
	tx := siptest.NewServerTxRecorder(invite)
	defer tx.Terminate()

	// Read Invite
	d, err := dialogSrv.ReadInvite(invite, tx)
	require.NoError(t, err)

	res200 := sip.NewResponseFromRequest(d.InviteRequest, 200, "OK", nil)
	ackReceive := newAckRequestUAC(d.InviteRequest, res200, nil)
	go func() {
		// Delay ACK receiving
		time.Sleep(2 * sip.T1)
		d.ReadAck(ackReceive, tx)
	}()
	// Respond 200
	// This will block until ACK
	err = d.WriteResponse(res200)
	require.NoError(t, err)

	// We must have at least 2 responses
	resps := tx.Result()
	require.Len(t, resps, 2)
}

func TestDialogServer2xxResponseContextCancellation(t *testing.T) {
	explicitCause := errors.New("answer canceled")
	tests := []struct {
		name       string
		newContext func() (context.Context, func(), func())
		want       error
	}{
		{
			name: "explicit cause",
			newContext: func() (context.Context, func(), func()) {
				ctx, cancel := context.WithCancelCause(context.Background())
				trigger := func() { cancel(explicitCause) }
				cleanup := func() { cancel(context.Canceled) }
				return ctx, trigger, cleanup
			},
			want: explicitCause,
		},
		{
			name: "deadline",
			newContext: func() (context.Context, func(), func()) {
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
				return ctx, func() {}, cancel
			},
			want: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dialog, tx := newDialogServerResponseTest(t)
			res200 := sip.NewResponseFromRequest(dialog.InviteRequest, 200, "OK", nil)
			ack := newAckRequestUAC(dialog.InviteRequest, res200, nil)
			established := make(chan struct{}, 1)
			dialog.OnState(func(state sip.DialogState) {
				if state == sip.DialogStateEstablished {
					select {
					case established <- struct{}{}:
					default:
					}
				}
			})
			ctx, trigger, cleanup := test.newContext()
			defer cleanup()
			result := make(chan error, 1)
			go func() { result <- dialog.WriteResponseContext(ctx, res200) }()
			select {
			case <-established:
			case <-time.After(time.Second):
				t.Fatal("dialog did not enter established state")
			}
			trigger()
			err := <-result
			require.ErrorIs(t, err, test.want)
			require.Equal(t, sip.DialogStateEnded, dialog.LoadState())
			require.ErrorIs(t, dialog.err(), test.want)
			require.ErrorIs(t, dialog.ReadAck(ack, tx), ErrDialogEnded)
			require.Equal(t, sip.DialogStateEnded, dialog.LoadState())
			require.NotEmpty(t, tx.Result())
			select {
			case <-tx.Done():
			case <-time.After(time.Second):
				t.Fatal("server transaction did not terminate")
			}
		})
	}
}

func TestDialogServerFinalFailureResponseContextCancellation(t *testing.T) {
	dialog, tx := newDialogServerResponseTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err := dialog.RespondContext(ctx, sip.StatusBusyHere, "Busy Here", nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, sip.DialogStateEnded, dialog.LoadState())
	require.ErrorIs(t, dialog.err(), context.DeadlineExceeded)
	require.NotEmpty(t, tx.Result())
}

func TestDialogServerACKAndContextCancellationConverge(t *testing.T) {
	for range 100 {
		dialog, tx := newDialogServerResponseTest(t)
		res200 := sip.NewResponseFromRequest(dialog.InviteRequest, 200, "OK", nil)
		ack := newAckRequestUAC(dialog.InviteRequest, res200, nil)
		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("concurrent answer cancellation")
		established := make(chan struct{}, 1)
		dialog.OnState(func(state sip.DialogState) {
			if state == sip.DialogStateEstablished {
				select {
				case established <- struct{}{}:
				default:
				}
			}
		})
		writeResult := make(chan error, 1)
		go func() { writeResult <- dialog.WriteResponseContext(ctx, res200) }()
		<-established
		start := make(chan struct{})
		ackResult := make(chan error, 1)
		cancelDone := make(chan struct{})
		go func() {
			<-start
			ackResult <- dialog.ReadAck(ack, tx)
		}()
		go func() {
			<-start
			cancel(cause)
			close(cancelDone)
		}()
		close(start)
		ackErr := <-ackResult
		<-cancelDone
		writeErr := <-writeResult

		switch dialog.LoadState() {
		case sip.DialogStateConfirmed:
			require.NoError(t, ackErr)
			require.NoError(t, writeErr)
		case sip.DialogStateEnded:
			require.ErrorIs(t, ackErr, ErrDialogEnded)
			require.ErrorIs(t, writeErr, cause)
		default:
			t.Fatalf("unexpected terminal dialog state: %s", dialog.LoadState())
		}
		tx.Terminate()
		require.NoError(t, dialog.Close())
	}
}

func TestDialogServerFinalResponseAndCANCELConverge(t *testing.T) {
	for range 25 {
		dialog, tx := newDialogServerResponseTest(t)
		res200 := sip.NewResponseFromRequest(dialog.InviteRequest, 200, "OK", nil)
		ack := newAckRequestUAC(dialog.InviteRequest, res200, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		start := make(chan struct{})
		writeResult := make(chan error, 1)
		cancelResult := make(chan error, 1)
		go func() {
			<-start
			writeResult <- dialog.WriteResponseContext(ctx, res200)
		}()
		go func() {
			<-start
			cancelResult <- tx.Receive(newCancelRequest(dialog.InviteRequest))
		}()
		close(start)
		if cancelErr := <-cancelResult; cancelErr != nil {
			require.ErrorIs(t, cancelErr, sip.ErrTransactionTerminated)
		}
		require.Error(t, <-writeResult)
		cancel()
		require.Equal(t, sip.DialogStateEnded, dialog.LoadState())
		require.ErrorIs(t, dialog.ReadAck(ack, tx), ErrDialogEnded)
		tx.Terminate()
		require.NoError(t, dialog.Close())
	}
}

func TestDialogServerWriteResponseRejectsNilContext(t *testing.T) {
	dialog, tx := newDialogServerResponseTest(t)
	res200 := sip.NewResponseFromRequest(dialog.InviteRequest, 200, "OK", nil)
	require.ErrorIs(t, dialog.WriteResponseContext(nil, res200), ErrDialogInvalidContext)
	require.Empty(t, tx.Result())
}

func TestDialogServerWriteResponseRejectsCanceledContextBeforeMutation(t *testing.T) {
	dialog, tx := newDialogServerResponseTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res200 := sip.NewResponseFromRequest(dialog.InviteRequest, 200, "OK", nil)
	require.ErrorIs(t, dialog.WriteResponseContext(ctx, res200), context.Canceled)
	require.Equal(t, sip.DialogStateInitial, dialog.LoadState())
	require.Nil(t, dialog.InviteResponse)
	require.Empty(t, tx.Result())
}

func newDialogServerResponseTest(t *testing.T) (*DialogServerSession, *siptest.ServerTxRecorder) {
	t.Helper()
	ua, err := NewUA()
	require.NoError(t, err)
	t.Cleanup(func() { _ = ua.Close() })
	client, err := NewClient(ua)
	require.NoError(t, err)
	server := NewDialogServerCache(client, sip.ContactHeader{
		Address: sip.Uri{User: "test", Host: "127.0.0.1", Port: 5099},
	})
	invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})
	tx := siptest.NewServerTxRecorder(invite)
	t.Cleanup(tx.Terminate)
	dialog, err := server.ReadInvite(invite, tx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dialog.Close() })
	return dialog, tx
}
