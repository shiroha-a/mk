package config

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// DBTLS is the resolved TLS setting for PostgreSQL connections, expressed in
// libpq terms so the same values can be written into a keyword/value DSN and
// into a postgres:// URL.
type DBTLS struct {
	// SSLMode is one of the libpq sslmode values.
	SSLMode string
	// SSLRootCert is a path to a PEM CA bundle, or empty to use the system
	// trust store (only meaningful for verify-ca / verify-full).
	SSLRootCert string
}

// validSSLModes lists the libpq sslmode values pgx accepts.
var validSSLModes = map[string]bool{
	"disable":     true,
	"allow":       true,
	"prefer":      true,
	"require":     true,
	"verify-ca":   true,
	"verify-full": true,
}

// ResolveDBTLS interprets db.extra and returns the TLS setting to use.
//
// Accepted keys (case-insensitive, because Viper lowercases map keys):
//
//   - ssl: bool (or a string that parses as one). true means TLS with
//     server certificate and hostname verification against the system CAs
//     (sslmode=verify-full), false means plaintext.
//   - ssl: a map in node-postgres form. Only rejectUnauthorized is honoured;
//     rejectUnauthorized: false means TLS without verification
//     (sslmode=require), anything else means verify-full.
//   - sslmode: an explicit libpq sslmode. Mutually exclusive with ssl.
//   - sslrootcert: a path to a PEM CA file used for verification.
//
// Unknown keys other than these are ignored, because upstream forwards
// db.extra to the node-postgres pool as-is and configs carry pool options
// (statement_timeout etc.) that mk-go does not use.
func ResolveDBTLS(extra map[string]any) (DBTLS, error) {
	var sslVal, modeVal, rootVal any
	var hasSSL, hasMode, hasRoot bool
	for k, v := range extra {
		switch strings.ToLower(k) {
		case "ssl":
			sslVal, hasSSL = v, true
		case "sslmode":
			modeVal, hasMode = v, true
		case "sslrootcert":
			rootVal, hasRoot = v, true
		}
	}

	var out DBTLS
	if hasRoot {
		s, ok := rootVal.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return DBTLS{}, fmt.Errorf("config: db.extra.sslrootcert must be a path to a PEM CA file, got %v", rootVal)
		}
		out.SSLRootCert = strings.TrimSpace(s)
	}

	if hasSSL && hasMode {
		return DBTLS{}, errors.New("config: db.extra.ssl and db.extra.sslmode are both set; use only one of them")
	}

	if hasMode {
		s, ok := modeVal.(string)
		mode := strings.ToLower(strings.TrimSpace(s))
		if !ok || !validSSLModes[mode] {
			return DBTLS{}, fmt.Errorf("config: db.extra.sslmode %v is not one of disable, allow, prefer, require, verify-ca, verify-full", modeVal)
		}
		out.SSLMode = mode
		if out.SSLRootCert != "" && mode == "disable" {
			return DBTLS{}, errors.New("config: db.extra.sslrootcert is set but db.extra.sslmode is disable")
		}
		return out, nil
	}

	mode, err := sslModeFromSSLValue(sslVal, hasSSL)
	if err != nil {
		return DBTLS{}, err
	}
	out.SSLMode = mode
	if out.SSLRootCert != "" {
		switch mode {
		case "disable":
			return DBTLS{}, errors.New("config: db.extra.sslrootcert is set but TLS is disabled; set db.extra.ssl: true")
		case "require":
			// pgx は require + sslrootcert を verify-ca として扱う (libpq 互換)。
			// rejectUnauthorized: false と書いた運営者の意図 (検証しない) と逆に
			// なるので、どちらを望んでいるか分からないまま黙って選ばない。
			return DBTLS{}, errors.New("config: db.extra.sslrootcert contradicts db.extra.ssl.rejectUnauthorized: false; remove one of them")
		}
	}
	return out, nil
}

// sslModeFromSSLValue maps the upstream-compatible db.extra.ssl value to an
// sslmode.
func sslModeFromSSLValue(v any, present bool) (string, error) {
	if !present || v == nil {
		return "disable", nil
	}
	switch t := v.(type) {
	case bool:
		return boolSSLMode(t), nil
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return "", fmt.Errorf("config: db.extra.ssl %q is not a boolean or a map", t)
		}
		return boolSSLMode(b), nil
	case map[string]any:
		return sslModeFromTLSOptions(t)
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[fmt.Sprint(k)] = val
		}
		return sslModeFromTLSOptions(m)
	default:
		return "", fmt.Errorf("config: db.extra.ssl has unsupported type %T; use true, false or { rejectUnauthorized: false }", v)
	}
}

func boolSSLMode(b bool) string {
	// upstream (node-postgres) の `ssl: true` は Node の TLS 既定 =
	// rejectUnauthorized: true なので、証明書とホスト名を検証する。
	// pgx の require は検証しない (InsecureSkipVerify) ため verify-full に寄せる。
	if b {
		return "verify-full"
	}
	return "disable"
}

// sslModeFromTLSOptions handles the node-postgres object form of ssl.
func sslModeFromTLSOptions(m map[string]any) (string, error) {
	verify := true
	for k, v := range m {
		switch strings.ToLower(k) {
		case "rejectunauthorized":
			b, err := anyToBool(v)
			if err != nil {
				return "", fmt.Errorf("config: db.extra.ssl.rejectUnauthorized: %w", err)
			}
			verify = b
		case "ca":
			// node-postgres は PEM の中身を受けるが、pgx の sslrootcert は
			// ファイルパスしか受けない。黙って無視すると検証先が system CA に
			// すり替わって接続できなくなるので、書き換え先を示して落とす。
			return "", errors.New("config: db.extra.ssl.ca (inline PEM) is not supported; save the CA certificate to a file and set db.extra.sslrootcert to its path")
		default:
			// cert / key / servername 等も黙って捨てると意図と違う接続になる。
			return "", fmt.Errorf("config: db.extra.ssl.%s is not supported (only rejectUnauthorized is); use db.extra.sslmode / db.extra.sslrootcert instead", k)
		}
	}
	if verify {
		return "verify-full", nil
	}
	return "require", nil
}

func anyToBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		if err != nil {
			return false, fmt.Errorf("%q is not a boolean", t)
		}
		return b, nil
	default:
		return false, fmt.Errorf("%v is not a boolean", v)
	}
}

// dbTLS returns the TLS setting derived from c.DB.Extra.
//
// Load は同じ関数で検証してから Config を返すので、ここでエラーになるのは
// Config を手で組み立てた場合だけ。そのときは最も厳しい verify-full に倒す
// (平文や無検証に倒すと、設定の誤りが盗聴可能な接続として表に出ない)。
func (c *Config) dbTLS() DBTLS {
	t, err := ResolveDBTLS(c.DB.Extra)
	if err != nil {
		return DBTLS{SSLMode: "verify-full"}
	}
	return t
}

// dsnQuote quotes a value for a libpq keyword/value connection string.
//
// 空文字・空白・引用符・バックスラッシュを含む値をそのまま埋めると、
// 後続の keyword と連結されて別の値として解釈される (空パスワードでは
// `password= dbname=x` が password="dbname=x" になる)。
func dsnQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\r\v\f'\\") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return "'" + r.Replace(s) + "'"
}

// buildKeywordDSN renders a libpq keyword/value DSN.
func buildKeywordDSN(host string, port int, user, pass, dbname string, t DBTLS) string {
	if IsUnixSocketPath(host) {
		// UDS では TLS を張れない。
		t = DBTLS{SSLMode: "disable"}
	}
	parts := []string{
		"host=" + dsnQuote(host),
		"port=" + strconv.Itoa(port),
		"user=" + dsnQuote(user),
		"password=" + dsnQuote(pass),
		"dbname=" + dsnQuote(dbname),
		"sslmode=" + t.SSLMode,
	}
	if t.SSLRootCert != "" {
		parts = append(parts, "sslrootcert="+dsnQuote(t.SSLRootCert))
	}
	return strings.Join(parts, " ")
}

// DSN returns the PostgreSQL connection string in libpq keyword/value form.
//
// Host が "/" で始まる場合は UNIX domain socket 接続とみなす。libpq / pgx の
// 慣例に従い、host にソケットディレクトリのパスを、port に対応する PG ポート番号
// (socket 名 .s.PGSQL.<port> の末尾数字) を渡す。UDS では TLS を張れないので
// sslmode は強制的に disable になる。TLS の設定は ResolveDBTLS を参照。
func (c *Config) DSN() string {
	return buildKeywordDSN(c.DB.Host, c.DB.Port, c.DB.User, c.DB.Pass, c.DB.DB, c.dbTLS())
}

// SlaveDSN returns the PostgreSQL connection string for the idx-th read
// replica declared in DBSlaves. idx が範囲外の場合は空文字列を返す。
//
// SSL/sslmode / Unix socket 判定は primary の DB 設定に合わせる。
// Misskey 本家では dbSlaves エントリ内で sslmode を個別指定する API は無いため、
// primary の extra を継承する (same-network / same-cluster replica を前提)。
func (c *Config) SlaveDSN(idx int) string {
	if idx < 0 || idx >= len(c.DBSlaves) {
		return ""
	}
	s := c.DBSlaves[idx]
	return buildKeywordDSN(s.Host, s.Port, s.User, s.Pass, s.DB, c.dbTLS())
}

// DatabaseURL returns the primary connection as a postgres URL with the given
// scheme (e.g. "pgx5" for golang-migrate). TLS settings and credential
// escaping match DSN.
func (c *Config) DatabaseURL(scheme string) string {
	t := c.dbTLS()
	q := url.Values{}
	u := &url.URL{Scheme: scheme, Path: "/" + c.DB.DB}
	if IsUnixSocketPath(c.DB.Host) {
		// postgres://...@/var/run/postgresql:5432/... の形式は UDS として
		// 解釈されず TCP localhost にフォールバックするため、ホスト部を空にして
		// host クエリにソケットディレクトリを渡す。
		q.Set("host", c.DB.Host)
		q.Set("port", strconv.Itoa(c.DB.Port))
		t = DBTLS{SSLMode: "disable"}
	} else {
		u.Host = net.JoinHostPort(c.DB.Host, strconv.Itoa(c.DB.Port))
	}
	u.User = url.UserPassword(c.DB.User, c.DB.Pass)
	q.Set("sslmode", t.SSLMode)
	if t.SSLRootCert != "" {
		q.Set("sslrootcert", t.SSLRootCert)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// DBTLSErrorHint returns an operator-facing hint when err is a TLS
// certificate verification failure, or "" otherwise.
func DBTLSErrorHint(err error) string {
	if err == nil {
		return ""
	}
	var (
		verr *tls.CertificateVerificationError
		uerr x509.UnknownAuthorityError
		herr x509.HostnameError
		cerr x509.CertificateInvalidError
	)
	if !errors.As(err, &verr) && !errors.As(err, &uerr) && !errors.As(err, &herr) && !errors.As(err, &cerr) {
		return ""
	}
	return "the PostgreSQL server certificate could not be verified. db.extra.ssl: true now verifies the certificate and hostname " +
		"against the system CAs (earlier mk-go versions did not). For a self-signed or private CA, set db.extra.sslrootcert to the CA file path; " +
		"to explicitly skip verification (exposes the connection to interception), set db.extra.ssl: { rejectUnauthorized: false }. See docs/configuration.md"
}
