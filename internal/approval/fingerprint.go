package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/guyi-a/Interview-Agent/internal/effect"
)

// Fingerprint identifies the exact repeatable operation the user saw.
// Safety-wall calls and sensitive writes can never be remembered.
func Fingerprint(e effect.Effect, argsJSON string) (string, bool) {
	if must, _ := MustAsk(e); must {
		return "", false
	}
	if sensitive, _ := IsSensitiveCall(e, argsJSON); sensitive {
		return "", false
	}
	switch e.Kind {
	case effect.KindFileRead, effect.KindFileWrite:
		if e.Path == "" {
			return "", false
		}
		return string(e.Kind) + ":" + filepath.Clean(e.Path), true
	case effect.KindFileTransfer:
		if e.Path == "" || e.DestPath == "" {
			return "", false
		}
		return strings.Join([]string{
			string(e.Kind), e.Operation,
			filepath.Clean(e.Path), filepath.Clean(e.DestPath),
		}, ":"), true
	case effect.KindProcessExec:
		if strings.TrimSpace(e.Command) == "" {
			return "", false
		}
		return strings.Join([]string{
			string(e.Kind), filepath.Clean(e.Cwd), e.Command,
		}, ":"), true
	case effect.KindNetwork:
		parsed, err := url.Parse(e.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", false
		}
		return string(e.Kind) + ":" + parsed.Scheme + "://" + parsed.Host, true
	case effect.KindMCPCall:
		if e.Server == "" || e.RemoteTool == "" {
			return "", false
		}
		return strings.Join([]string{string(e.Kind), e.Server, e.RemoteTool}, ":"), true
	default:
		return "", false
	}
}

func ParseEffect(raw string) (effect.Effect, bool) {
	var e effect.Effect
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &e) != nil || e.Kind == "" {
		return effect.Effect{}, false
	}
	return e, true
}

func EffectDigest(e effect.Effect) string {
	raw, _ := json.Marshal(e)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
