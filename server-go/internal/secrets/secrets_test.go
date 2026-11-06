package secrets

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSealAndOpen(t *testing.T) {
	oldKey, newKey := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	old, err := New(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Seal([]byte("hunter2"), "proxy.password:1")
	if err != nil || !IsSealed(sealed) || strings.Contains(sealed, "hunter2") {
		t.Fatalf("sealed: %q %v", sealed, err)
	}
	if again, _ := old.Seal([]byte("hunter2"), "proxy.password:1"); again == sealed {
		t.Error("two seals of the same value are the same")
	}
	if plain, err := old.Open(sealed, "proxy.password:1"); err != nil || string(plain) != "hunter2" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := old.Open(sealed, "proxy.password:2"); err == nil {
		t.Error("a value moved to another row opened")
	}
	if plain, err := old.Open("not sealed", "x"); err != nil || string(plain) != "not sealed" {
		t.Errorf("plain: %q %v", plain, err)
	}
	var none *Keyring
	if _, err := none.Open(sealed, "proxy.password:1"); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
	if v, _ := none.Seal([]byte("x"), "a"); v != "x" {
		t.Errorf("a nil keyring sealed: %q", v)
	}

	rotated, err := New(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := rotated.Open(sealed, "proxy.password:1"); err != nil || string(plain) != "hunter2" {
		t.Fatalf("open with the old key in the ring: %q %v", plain, err)
	}
	resealed, _ := rotated.Seal([]byte("hunter2"), "proxy.password:1")
	if KeyID(resealed) != rotated.CurrentID() || KeyID(sealed) == rotated.CurrentID() {
		t.Errorf("key ids: %s %s %s", KeyID(resealed), KeyID(sealed), rotated.CurrentID())
	}
	onlyNew, _ := New(newKey)
	if _, err := onlyNew.Open(sealed, "proxy.password:1"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("without the old key: %v", err)
	}
	if _, err := New([]byte("short")); err == nil {
		t.Error("a short key was taken")
	}
}

func TestParseKey(t *testing.T) {
	for _, text := range []string{
		"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		"AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE",
		strings.Repeat("01", 32),
	} {
		if k, err := ParseKey(text); err != nil || !bytes.Equal(k, bytes.Repeat([]byte{1}, 32)) {
			t.Errorf("%s: %x %v", text, k, err)
		}
	}
	if _, err := ParseKey("too short"); err == nil {
		t.Error("a bad key was taken")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("KUMA_SECRET_KEY", "")
	if k, err := FromEnv(); k != nil || err != nil {
		t.Fatalf("without a key: %v %v", k, err)
	}
	t.Setenv("KUMA_SECRET_KEY", strings.Repeat("02", 32))
	t.Setenv("KUMA_SECRET_KEY_OLD", strings.Repeat("01", 32)+", "+strings.Repeat("03", 32))
	k, err := FromEnv()
	if err != nil || len(k.byID) != 3 {
		t.Fatalf("with old keys: %v %v", k, err)
	}
}
