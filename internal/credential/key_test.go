package credential

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"tinysync/internal/credential/keytest"
)

// 固定测试钥匙（ssh-keygen 生成，ed25519）：同一把钥匙的明文与加密
// 形态，公钥指纹恒为 testKeyFingerprint。精确断言指纹算法与格式。
const (
	testKeyUnencrypted = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBevv1jThlT4cPrwg07ozpT4ZLf1N/NnGxM5A1i2+KjEAAAAJD+Oas2/jmr
NgAAAAtzc2gtZWQyNTUxOQAAACBevv1jThlT4cPrwg07ozpT4ZLf1N/NnGxM5A1i2+KjEA
AAAEA9XYYH83y5RuDkoXWGhlTYZKuRdGNf9L4vp2bTCGT4116+/WNOGVPhw+vCDTujOlPh
kt/U382cbEzkDWLb4qMQAAAADXRpbnlzeW5jLXRlc3Q=
-----END OPENSSH PRIVATE KEY-----
`

	testKeyEncrypted = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAACmFlczI1Ni1jdHIAAAAGYmNyeXB0AAAAGAAAABDretLtGk
xykZ1mBMVqwli9AAAAGAAAAAEAAAAzAAAAC3NzaC1lZDI1NTE5AAAAIF6+/WNOGVPhw+vC
DTujOlPhkt/U382cbEzkDWLb4qMQAAAAkL9D8J9/3R7+HF9hjT2NINvyPSdeexIxppbk6N
XuJLwZUnKExCeEArGeqUSoJyqQs/6UzOfvVkk26iGbCUvfWutGMNqst3RLWpExiKIdw93u
rcn8Fsm+0qO0uxBefJLqHmkoPIXigHWxe57/K1tl4BbP1u4LrqhnS7wPG0V14xmtL/5NyS
QLWCnN1iXvIp5ZXQ==
-----END OPENSSH PRIVATE KEY-----
`

	testKeyPassphrase = "test-passphrase"
	// testKeyFingerprint 与 ssh-keygen -lf 输出一致（同一把公钥）。
	testKeyFingerprint = "SHA256:OJ9FPyy9KMBFzGfsIHU+6ahpI7MyCAmGhlvr+Vlu7DQ"
)

// 未加密钥匙解析成功，指纹与 ssh-keygen 完全一致。
func TestParseSecretUnencrypted(t *testing.T) {
	fp, err := ParseSecret(Secret{PrivateKey: testKeyUnencrypted})
	if err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
	if fp != testKeyFingerprint {
		t.Errorf("fingerprint = %q, want %q", fp, testKeyFingerprint)
	}
}

// 加密钥匙以正确口令解析成功，指纹与明文形态一致。
func TestParseSecretEncryptedWithCorrectPassphrase(t *testing.T) {
	fp, err := ParseSecret(Secret{PrivateKey: testKeyEncrypted, PrivateKeyPassphrase: testKeyPassphrase})
	if err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
	if fp != testKeyFingerprint {
		t.Errorf("fingerprint = %q, want %q", fp, testKeyFingerprint)
	}
}

// 加密钥匙缺口令：入口拒绝，不进入解析。
func TestParseSecretEncryptedMissingPassphrase(t *testing.T) {
	_, err := ParseSecret(Secret{PrivateKey: testKeyEncrypted})
	if !errIsInvalidWith(err, "passphrase is required") {
		t.Errorf("err = %v, want ErrInvalid with passphrase required", err)
	}
}

// 加密钥匙口令错误：拒绝。
func TestParseSecretEncryptedWrongPassphrase(t *testing.T) {
	_, err := ParseSecret(Secret{PrivateKey: testKeyEncrypted, PrivateKeyPassphrase: "wrong"})
	if !errIsInvalidWith(err, "incorrect private key passphrase") {
		t.Errorf("err = %v, want ErrInvalid with incorrect passphrase", err)
	}
}

// 未加密钥匙附带口令：拒绝——存入永远用不上的口令只会造成误解。
func TestParseSecretUnencryptedWithPassphrase(t *testing.T) {
	_, err := ParseSecret(Secret{PrivateKey: testKeyUnencrypted, PrivateKeyPassphrase: "x"})
	if !errIsInvalidWith(err, "must be empty") {
		t.Errorf("err = %v, want ErrInvalid with passphrase must be empty", err)
	}
}

// 空私钥拒绝。
func TestParseSecretEmpty(t *testing.T) {
	_, err := ParseSecret(Secret{})
	if !errIsInvalidWith(err, "private_key is required") {
		t.Errorf("err = %v, want ErrInvalid with private_key required", err)
	}
}

// 非 PEM 内容拒绝。
func TestParseSecretNotPEM(t *testing.T) {
	_, err := ParseSecret(Secret{PrivateKey: "not a pem at all"})
	if !errIsInvalidWith(err, "PEM-encoded") {
		t.Errorf("err = %v, want ErrInvalid with PEM-encoded", err)
	}
}

// 公钥（而非私钥）拒绝。
func TestParseSecretPublicKeyRejected(t *testing.T) {
	_, err := ParseSecret(Secret{PrivateKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIB6+/WNOGVPhw+vCDTujOlPhkt/U382cbEzkDWLb4qMQ tinysync-test\n"})
	if err == nil {
		t.Fatalf("ParseSecret with public key: want error, got nil")
	}
}

// 随机生成的 rsa / ed25519 钥匙（keytest 形态）均可解析；指纹稳定。
func TestParseSecretGeneratedKeyTypes(t *testing.T) {
	rsaPEM := generateTestRSAPEM(t)
	rsaFP, err := ParseSecret(Secret{PrivateKey: rsaPEM})
	if err != nil {
		t.Fatalf("ParseSecret rsa: %v", err)
	}
	if !strings.HasPrefix(rsaFP, "SHA256:") {
		t.Errorf("rsa fingerprint = %q, want SHA256: prefix", rsaFP)
	}

	edPEM, err := keytest.UnencryptedEd25519()
	if err != nil {
		t.Fatalf("keytest.UnencryptedEd25519: %v", err)
	}
	edFP1, err := ParseSecret(Secret{PrivateKey: edPEM})
	if err != nil {
		t.Fatalf("ParseSecret ed25519: %v", err)
	}
	edFP2, err := ParseSecret(Secret{PrivateKey: edPEM})
	if err != nil {
		t.Fatalf("ParseSecret ed25519 again: %v", err)
	}
	if edFP1 != edFP2 || !strings.HasPrefix(edFP1, "SHA256:") {
		t.Errorf("ed25519 fingerprints %q / %q: want stable SHA256: values", edFP1, edFP2)
	}
}

// keytest 的加密钥匙形态（bcrypt KDF）可用正确口令解开。
func TestParseSecretKeytestEncrypted(t *testing.T) {
	pemStr, err := keytest.EncryptedEd25519("another-pass")
	if err != nil {
		t.Fatalf("keytest.EncryptedEd25519: %v", err)
	}
	if _, err := ParseSecret(Secret{PrivateKey: pemStr, PrivateKeyPassphrase: "another-pass"}); err != nil {
		t.Fatalf("ParseSecret: %v", err)
	}
}

// errIsInvalidWith 断言 err 链上带 ErrInvalid 且消息包含 want。
func errIsInvalidWith(err error, want string) bool {
	return errors.Is(err, ErrInvalid) && strings.Contains(err.Error(), want)
}

// generateTestRSAPEM 生成未加密 RSA 私钥 PEM（PKCS#1）。
func generateTestRSAPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}))
}
