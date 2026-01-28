package testutils_test

import (
	"io"
	"strings"
	"testing"

	"github.com/Kodecable/crosspty/internal/testutils"
)

func TestANSIStripper(t *testing.T) {
	rawOutput := "Hello \x1b[31mRed\x1b[0m World\x1b[1A!"
	expected := "Hello Red World!"

	reader := testutils.NewANSIStripper(strings.NewReader(rawOutput))
	result, _ := io.ReadAll(reader)

	if string(result) != expected {
		t.Errorf("Expected '%s', got '%s'", expected, string(result))
	}
}

func TestANSIStripper_OSC(t *testing.T) {
	raw := "Hello \x1b]0;Skip Me\x07World\x1b[31m!\x1b[0m"
	expected := "Hello World!"

	reader := testutils.NewANSIStripper(strings.NewReader(raw))
	result, _ := io.ReadAll(reader)

	if string(result) != expected {
		t.Errorf("Expected '%s', got '%s'", expected, string(result))
	}

	rawST := "Start\x1b]0;Title with ST\x1b\\End"
	expectedST := "StartEnd"

	readerST := testutils.NewANSIStripper(strings.NewReader(rawST))
	resultST, _ := io.ReadAll(readerST)

	if string(resultST) != expectedST {
		t.Errorf("Expected '%s', got '%s'", expectedST, string(resultST))
	}
}
