package telegram

import (
	"testing"
)

func TestSelectionLabelPersian(t *testing.T) {
	if got := selectionLabel([]string{"XAU/USD", "5min", "scalp"}); got != "طلا/دلار · ۵ دقیقه · اسکالپ" {
		t.Fatal(got)
	}
}
