package ui

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestCDATACantBeClosedEarly(t *testing.T) {
	out, err := xml.Marshal(rssItem{Title: cdata{"a]]><script>x</script>"}})
	if err != nil {
		t.Fatal(err)
	}
	var back rssItem
	if err := xml.Unmarshal(out, &back); err != nil || back.Title.Text != "a]]><script>x</script>" {
		t.Fatalf("round trip: %q %v from %s", back.Title.Text, err, out)
	}
	if strings.Contains(string(out), "]]><script>") {
		t.Fatalf("the title closed the CDATA section: %s", out)
	}
}
