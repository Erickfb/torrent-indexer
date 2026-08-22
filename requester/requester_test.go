package requester

import "testing"

func TestHasChallangeIgnoresCloudflareJSScriptOnContentPage(t *testing.T) {
	body := []byte(`<html><body><article class="post">Release content</article><script src="/cdn-cgi/challenge-platform/scripts/jsd/main.js"></script></body></html>`)

	if hasChallange(body) {
		t.Fatal("hasChallange() detected a Cloudflare challenge on a valid content page")
	}
}

func TestHasChallangeDetectsChallengePage(t *testing.T) {
	tests := []string{
		`<html><head><title>Just a moment...</title></head></html>`,
		`<html><body>Website is under attack</body></html>`,
		`<html><body data-cf-mitigated="challenge"></body></html>`,
		`<script src="/cdn-cgi/challenge-platform/h/b/cf-chl-bypass.js"></script>`,
	}

	for _, body := range tests {
		if !hasChallange([]byte(body)) {
			t.Fatalf("hasChallange() did not detect challenge in %q", body)
		}
	}
}
