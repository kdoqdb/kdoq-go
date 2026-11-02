package wire

// ErrorFrame is the decoded ERROR frame.
type ErrorFrame struct {
	RequestID uint64
	Code      uint16
	// Unknown is true when the outcome field is 1.
	Unknown bool
	Message string
}

// ParseError decodes the payload of an ERROR frame.
func ParseError(payload []byte) (ErrorFrame, error) {
	f, err := Parse(payload)
	if err != nil {
		return ErrorFrame{}, err
	}
	var e ErrorFrame
	e.RequestID, _, err = f.U64(1)
	if err != nil {
		return e, err
	}
	if e.Code, err = f.RequireU16(2); err != nil {
		return e, err
	}
	outcome, _, err := f.U16(3)
	if err != nil {
		return e, err
	}
	e.Unknown = outcome == 1
	e.Message, _, err = f.String(4)
	return e, err
}

// EncodeError builds an ERROR frame (used by servers and tests).
func EncodeError(e ErrorFrame) []byte {
	var b Builder
	b.U64(1, e.RequestID).U16(2, e.Code)
	if e.Unknown {
		b.U16(3, 1)
	} else {
		b.U16(3, 0)
	}
	if e.Message != "" {
		b.SkippableString(4, e.Message)
	}
	return Encode(KindError, b.Bytes())
}
