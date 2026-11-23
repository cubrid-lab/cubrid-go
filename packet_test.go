package cubrid

import (
	"errors"
	"math"
	"testing"
)

func TestCheckRequestLength(t *testing.T) {
	const header = SizeDataLength + SizeCASInfo
	for _, packetLen := range []int64{header, header + 1, header + math.MaxInt32} {
		if err := checkRequestLength(packetLen); err != nil {
			t.Errorf("checkRequestLength(%d) = %v, want nil", packetLen, err)
		}
	}
	for _, packetLen := range []int64{header + math.MaxInt32 + 1, header + math.MaxUint32 + 1} {
		err := checkRequestLength(packetLen)
		var pe *ProgrammingError
		if !errors.As(err, &pe) {
			t.Errorf("checkRequestLength(%d) = %v, want *ProgrammingError", packetLen, err)
		}
	}
}
