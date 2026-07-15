package pocket48

import "testing"

func TestEncryptPasswordMatchesAndroid7143(t *testing.T) {
	got, err := EncryptPassword("password123")
	if err != nil {
		t.Fatalf("EncryptPassword() error = %v", err)
	}
	const want = "NUnOF5pyLLQpoIsuSbx4RA=="
	if got != want {
		t.Fatalf("EncryptPassword() = %q, want %q", got, want)
	}
}

func TestEncryptPasswordEmptyUsesFullPaddingBlock(t *testing.T) {
	got, err := EncryptPassword("")
	if err != nil {
		t.Fatalf("EncryptPassword() error = %v", err)
	}
	if got == "" {
		t.Fatal("empty password must still produce a PKCS#7 padded block")
	}
}
