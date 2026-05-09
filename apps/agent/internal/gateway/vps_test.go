package gateway

import "testing"

func TestParseVNStatCounters(t *testing.T) {
	data := []byte(`{
	  "interfaces": [
	    {"name": "eth0", "traffic": {"total": {"rx": 1000, "tx": 2000}}},
	    {"name": "lo", "traffic": {"total": {"rx": 10, "tx": 20}}}
	  ]
	}`)

	counters, err := ParseVNStatCounters(data)
	if err != nil {
		t.Fatalf("ParseVNStatCounters returned error: %v", err)
	}
	if counters["eth0"].RX != 1000 || counters["eth0"].TX != 2000 {
		t.Fatalf("unexpected eth0 counters: %+v", counters["eth0"])
	}
}

func TestParseSSEstablished(t *testing.T) {
	data := []byte(`ESTAB 0 0 45.62.113.232:25629 171.88.21.177:51820
ESTAB 0 0 [::ffff:45.62.113.232]:59962 [::ffff:171.88.21.178]:51821
ESTAB 0 0 45.62.113.232:22 1.1.1.1:12345`)

	entries := ParseSSEstablished(data, []string{"25629", "59962"})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].SourceIP != "171.88.21.177" || entries[0].LocalPort != "25629" {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
}

func TestParseHysteriaJournal(t *testing.T) {
	data := []byte(`2026-05-09T10:00:00+08:00 host hysteria-server[1]: addr=171.88.21.177:51820 reqAddr=youtube.com:443
2026-05-09T10:00:01+08:00 host hysteria-server[1]: no useful fields`)

	events := ParseHysteriaJournal(data)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].SourceIP != "171.88.21.177" || events[0].Target != "youtube.com:443" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}

func TestCategorizeDomain(t *testing.T) {
	if got := categorizeDomain("r1---sn.googlevideo.com", "Hysteria"); got != "YouTube" {
		t.Fatalf("expected YouTube, got %s", got)
	}
	if got := categorizeDomain("api.github.com", "Hysteria"); got != "GitHub" {
		t.Fatalf("expected GitHub, got %s", got)
	}
}
