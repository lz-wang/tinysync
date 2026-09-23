package credential

import (
	"crypto"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ParseSecret 校验 ssh_key secret：解析 PEM 私钥并派生公钥 SHA256
// 指纹。坏钥在入口失败，不污染运行时。规则（fail-closed）：
//   - private_key 必填且必须是可解析的 PEM 私钥；
//   - 加密钥匙必须提供口令，未加密钥匙必须不带口令——两者错配都
//     拒绝，不允许存入「永远用不上」或「永远解不开」的口令；
//   - 口令错误拒绝；
//   - 公钥指纹（SHA256:<base64>）只用于人前区分钥匙，与主机密钥
//     指纹（校验连接目标）无关。
func ParseSecret(s Secret) (string, error) {
	if strings.TrimSpace(s.PrivateKey) == "" {
		return "", fmt.Errorf("%w: private_key is required", ErrInvalid)
	}
	block, _ := pem.Decode([]byte(s.PrivateKey))
	if block == nil {
		return "", fmt.Errorf("%w: private_key is not a PEM-encoded key", ErrInvalid)
	}
	encrypted, known := keyEncrypted(block)
	if !known {
		return "", fmt.Errorf("%w: unsupported private key block %q", ErrInvalid, block.Type)
	}
	switch {
	case encrypted && s.PrivateKeyPassphrase == "":
		return "", fmt.Errorf("%w: private key is encrypted; private_key_passphrase is required", ErrInvalid)
	case !encrypted && s.PrivateKeyPassphrase != "":
		return "", fmt.Errorf("%w: private key is not encrypted; private_key_passphrase must be empty", ErrInvalid)
	}

	var (
		priv any
		err  error
	)
	if encrypted {
		// 加密钥匙：带口令解析。明文钥匙走此路径会得到「not password
		// protected」，但形态已在上方错配检查中拒绝，不会到达这里。
		priv, err = ssh.ParseRawPrivateKeyWithPassphrase([]byte(s.PrivateKey), []byte(s.PrivateKeyPassphrase))
	} else {
		priv, err = ssh.ParseRawPrivateKey([]byte(s.PrivateKey))
	}
	if err != nil {
		var missing *ssh.PassphraseMissingError
		switch {
		case errors.As(err, &missing), errors.Is(err, x509.IncorrectPasswordError):
			return "", fmt.Errorf("%w: incorrect private key passphrase", ErrInvalid)
		default:
			return "", fmt.Errorf("%w: parse private key: %v", ErrInvalid, err)
		}
	}
	pub, err := publicKeyOf(priv)
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(pub), nil
}

// keyEncrypted 判断 PEM 块是否为加密私钥；known=false 表示无法识别
// 的块类型，交由调用方拒绝。
func keyEncrypted(block *pem.Block) (encrypted, known bool) {
	if block.Type == "OPENSSH PRIVATE KEY" {
		// 新式 openssh 容器：KDF 信息在容器二进制头里（magic 之后的
		// ciphername），不在 PEM 头里；ciphername 非 none 即加密。
		return opensshEncrypted(block.Bytes)
	}
	if block.Headers["Proc-Type"] == "4,ENCRYPTED" {
		// 传统加密 PEM（ssh-keygen -m PEM 加密产物）。
		return true, true
	}
	switch block.Type {
	case "RSA PRIVATE KEY", "EC PRIVATE KEY", "PRIVATE KEY":
		return false, true
	}
	return false, false
}

// opensshMagic 是 openssh 私钥容器的固定文件头。
const opensshMagic = "openssh-key-v1\x00"

// opensshEncrypted 解析 openssh 容器头：magic + ciphername + kdfname
// + kdbopts + nkeys（各 string 均 uint32 长度前缀）；ciphername 非
// "none" 即加密。结构不完整按未知处理，由调用方拒绝。
func opensshEncrypted(der []byte) (encrypted, known bool) {
	if len(der) < len(opensshMagic) || string(der[:len(opensshMagic)]) != opensshMagic {
		return false, false
	}
	cipher, ok := readSSHString(der[len(opensshMagic):])
	if !ok {
		return false, false
	}
	return string(cipher) != "none", true
}

// readSSHString 读取一个 uint32 长度前缀的 string；越界返回 false。
// 长度以 uint64 比较，32 位平台上超大 n 不会因 int 溢出绕过检查。
func readSSHString(b []byte) ([]byte, bool) {
	if len(b) < 4 {
		return nil, false
	}
	n := binary.BigEndian.Uint32(b[:4])
	if uint64(len(b)-4) < uint64(n) {
		return nil, false
	}
	return b[4 : 4+n], true
}

// publicKeyOf 从解析出的私钥提取公钥。各解析路径返回 *rsa / *ecdsa /
// ed25519（openssh 容器为 *ed25519）形态的钥匙，均实现 crypto.Signer；
// 不支持的类型（含 DSA）在入口整体拒绝。
func publicKeyOf(priv any) (ssh.PublicKey, error) {
	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%w: unsupported private key type %T", ErrInvalid, priv)
	}
	sshPub, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		return nil, fmt.Errorf("%w: unsupported private key: %v", ErrInvalid, err)
	}
	return sshPub, nil
}
