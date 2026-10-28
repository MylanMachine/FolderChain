package engine

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"
)

// A share key is 20 random bytes (160 bits), shown as 8 groups of 4 base32
// characters. Every device in the share holds it; it is never sent over the
// network, only used to prove knowledge of it.
const shareKeyBytes = 20

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func NewShareKey() string {
	b := make([]byte, shareKeyBytes)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return formatShareKey(b)
}

func formatShareKey(b []byte) string {
	s := b32.EncodeToString(b)
	var parts []string
	for i := 0; i < len(s); i += 4 {
		parts = append(parts, s[i:min(i+4, len(s))])
	}
	return strings.Join(parts, "-")
}

// ParseShareKey accepts a key with or without dashes/spaces, in any case.
func ParseShareKey(s string) ([]byte, error) {
	var clean strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7') {
			clean.WriteRune(r)
		}
	}
	b, err := b32.DecodeString(clean.String())
	if err != nil || len(b) != shareKeyBytes {
		return nil, errors.New("that share key doesn't look right — it should be 8 groups of 4 letters/numbers")
	}
	return b, nil
}

func NormalizeShareKey(s string) (string, error) {
	b, err := ParseShareKey(s)
	if err != nil {
		return "", err
	}
	return formatShareKey(b), nil
}

type shareKeys struct {
	shareID string // public identifier used in discovery
	auth    []byte // proves membership during the connection handshake
	disco   []byte // authenticates discovery announcements
}

func deriveKeys(secret []byte) shareKeys {
	d := func(label string) []byte {
		m := hmac.New(sha256.New, secret)
		m.Write([]byte("folderchain/v1/" + label))
		return m.Sum(nil)
	}
	return shareKeys{
		shareID: hex.EncodeToString(d("share-id")[:8]),
		auth:    d("auth"),
		disco:   d("discovery"),
	}
}

func newDeviceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToUpper(hex.EncodeToString(b))
}

// newTLSCert makes a throwaway self-signed certificate. Identity comes from
// the share-key proof below, not from the certificate, so it is regenerated
// each run.
func newTLSCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "folderchain"},
		NotBefore:    time.Now().Add(-24 * time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

const alpn = "folderchain/1"

func serverTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, NextProtos: []string{alpn}}
}

func clientTLS(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{alpn},
		// Certificates are not used for identity; both sides instead prove
		// knowledge of the share key bound to this exact TLS session
		// (exportAuth), which also defeats man-in-the-middle relays.
		InsecureSkipVerify: true,
	}
}

type authHello struct {
	ID      string
	Name    string
	Port    int
	Version string
	Proof   []byte
}

func proof(key []byte, ekm []byte, role, id string) []byte {
	m := hmac.New(sha256.New, key)
	fmt.Fprintf(m, "folderchain-proof|%s|%s|", role, id)
	m.Write(ekm)
	return m.Sum(nil)
}

var errBadKey = errors.New("device is using a different share key")

// exchangeAuth runs after the TLS handshake. Each side sends its device info
// and an HMAC over the session's exported keying material, then verifies the
// other side's.
func exchangeAuth(c *tls.Conn, key []byte, me authHello, client bool) (*authHello, error) {
	st := c.ConnectionState()
	ekm, err := st.ExportKeyingMaterial("folderchain-auth", nil, 32)
	if err != nil {
		return nil, err
	}
	myRole, theirRole := "s", "c"
	if client {
		myRole, theirRole = "c", "s"
	}
	me.Proof = proof(key, ekm, myRole, me.ID)
	b, _ := json.Marshal(me)
	frame := make([]byte, 6+len(b))
	copy(frame, "FC1\x00")
	binary.BigEndian.PutUint16(frame[4:], uint16(len(b)))
	copy(frame[6:], b)
	errc := make(chan error, 1)
	go func() { _, err := c.Write(frame); errc <- err }()

	hdr := make([]byte, 6)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return nil, err
	}
	if string(hdr[:4]) != "FC1\x00" {
		return nil, errors.New("not a FolderChain device")
	}
	body := make([]byte, binary.BigEndian.Uint16(hdr[4:]))
	if _, err := io.ReadFull(c, body); err != nil {
		return nil, err
	}
	if err := <-errc; err != nil {
		return nil, err
	}
	var them authHello
	if err := json.Unmarshal(body, &them); err != nil {
		return nil, err
	}
	if !hmac.Equal(them.Proof, proof(key, ekm, theirRole, them.ID)) {
		return nil, errBadKey
	}
	if them.ID == "" || len(them.ID) > 64 || len(them.Name) > 128 {
		return nil, errors.New("invalid device info")
	}
	return &them, nil
}

func announceMAC(key []byte, parts ...string) string {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil)[:16])
}
