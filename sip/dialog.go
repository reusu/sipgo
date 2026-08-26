package sip

type DialogState int

const (
	// DialogStateInitial is a newly created dialog before its final INVITE response.
	DialogStateInitial DialogState = 0
	// Dialog received 200 response
	DialogStateEstablished DialogState = 1
	// Dialog received ACK
	DialogStateConfirmed DialogState = 2
	// Dialog received BYE
	DialogStateEnded DialogState = 3
)

func (s DialogState) String() string {
	switch s {
	case DialogStateInitial:
		return "Initial"
	case DialogStateEstablished:
		return "Established"
	case DialogStateConfirmed:
		return "Confirmed"
	case DialogStateEnded:
		return "Ended"
	default:
		return "Unknown Dialog State"
	}
}
