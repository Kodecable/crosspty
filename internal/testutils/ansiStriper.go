package testutils

import (
	"io"
)

type ANSIStripper struct {
	Source io.Reader
	state  stripState
}

type stripState int

const (
	stripStateNormal stripState = iota
	stripStateEsc
	stripStateCSI
	stripStateOSC
	stripStateOscEsc
)

func NewANSIStripper(r io.Reader) *ANSIStripper {
	return &ANSIStripper{
		Source: r,
		state:  stripStateNormal,
	}
}

func (s *ANSIStripper) Read(p []byte) (int, error) {
	n, err := s.Source.Read(p)
	if n == 0 {
		return 0, err
	}

	writePtr := 0
	for readPtr := range n {
		b := p[readPtr]

		switch s.state {
		case stripStateNormal:
			if b == 0x1B {
				s.state = stripStateEsc
			} else {
				p[writePtr] = b
				writePtr++
			}

		case stripStateEsc:
			switch b {
			case '[':
				s.state = stripStateCSI
			case ']':
				s.state = stripStateOSC
			default:
				// ignore
				s.state = stripStateNormal
			}

		case stripStateCSI:
			// CSI end: 0x40-0x7E
			if b >= 0x40 && b <= 0x7E {
				s.state = stripStateNormal
			}

		case stripStateOSC:
			switch b {
			case 0x07:
				s.state = stripStateNormal
			case 0x1B:
				// ST (ESC \)
				s.state = stripStateOscEsc
			}

		case stripStateOscEsc:
			// ST (ESC \)
			if b == '\\' {
				s.state = stripStateNormal
			} else {
				switch b {
				case '[':
					s.state = stripStateCSI
				case ']':
					s.state = stripStateOSC
				default:
					s.state = stripStateNormal
				}
			}
		}
	}

	return writePtr, err
}
