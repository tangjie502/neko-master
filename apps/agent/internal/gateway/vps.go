package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/foru17/neko-master/apps/agent/internal/domain"
)

type VPSConfig struct {
	Interfaces      []string
	TCPPorts        []string
	HysteriaService string
	HysteriaPort    string
}

type vpsCollector struct {
	config       VPSConfig
	lastIface    map[string]vpsIfaceCounter
	seenHysteria map[string]struct{}
}

type vpsIfaceCounter struct {
	RX int64
	TX int64
}

func NewVPSCollector(config VPSConfig) *vpsCollector {
	return &vpsCollector{
		config:       config,
		lastIface:    make(map[string]vpsIfaceCounter),
		seenHysteria: make(map[string]struct{}),
	}
}

func (v *vpsCollector) Collect(ctx context.Context) ([]domain.FlowSnapshot, error) {
	nowMs := time.Now().UnixMilli()
	var out []domain.FlowSnapshot

	ifaceUpdates, err := v.collectVNStat(ctx, nowMs)
	if err == nil {
		out = append(out, ifaceUpdates...)
	}

	ssUpdates, err := v.collectSS(ctx, nowMs)
	if err == nil {
		out = append(out, ssUpdates...)
	}

	journalUpdates, err := v.collectHysteriaJournal(ctx, nowMs)
	if err == nil {
		out = append(out, journalUpdates...)
	}

	if err != nil && len(out) == 0 {
		return nil, err
	}
	return out, nil
}

func (v *vpsCollector) collectVNStat(ctx context.Context, nowMs int64) ([]domain.FlowSnapshot, error) {
	output, err := runCommand(ctx, "vnstat", "--json")
	if err != nil {
		return nil, err
	}
	counters, err := ParseVNStatCounters(output)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]struct{}, len(v.config.Interfaces))
	for _, iface := range v.config.Interfaces {
		if strings.TrimSpace(iface) != "" {
			allowed[strings.TrimSpace(iface)] = struct{}{}
		}
	}

	out := make([]domain.FlowSnapshot, 0, len(counters))
	for iface, current := range counters {
		if len(allowed) > 0 {
			if _, ok := allowed[iface]; !ok {
				continue
			}
		}

		prev, ok := v.lastIface[iface]
		v.lastIface[iface] = current
		if !ok {
			continue
		}

		download := positiveDelta(current.RX, prev.RX)
		upload := positiveDelta(current.TX, prev.TX)
		if upload == 0 && download == 0 {
			continue
		}

		out = append(out, domain.FlowSnapshot{
			ID:          "vps-total-" + iface,
			Domain:      "",
			IP:          "",
			SourceIP:    "",
			Chains:      []string{"VPS-total"},
			Rule:        "VPS",
			RulePayload: iface,
			Upload:      upload,
			Download:    download,
			Connections: 0,
			TimestampMs: nowMs,
		})
	}
	return out, nil
}

func (v *vpsCollector) collectSS(ctx context.Context, nowMs int64) ([]domain.FlowSnapshot, error) {
	if len(v.config.TCPPorts) == 0 {
		return nil, nil
	}
	output, err := runCommand(ctx, "ss", "-Hnt", "state", "established")
	if err != nil {
		return nil, err
	}
	entries := ParseSSEstablished(output, v.config.TCPPorts)
	out := make([]domain.FlowSnapshot, 0, len(entries))
	for _, entry := range entries {
		chain := "Xray-" + entry.LocalPort
		out = append(out, domain.FlowSnapshot{
			ID:          "vps-ss-" + entry.LocalPort + "-" + entry.SourceIP,
			SourceIP:    entry.SourceIP,
			Chains:      []string{chain},
			Rule:        "Xray",
			RulePayload: "tcp:" + entry.LocalPort,
			Upload:      1,
			Download:    0,
			Connections: 1,
			TimestampMs: nowMs,
		})
	}
	return out, nil
}

func (v *vpsCollector) collectHysteriaJournal(ctx context.Context, nowMs int64) ([]domain.FlowSnapshot, error) {
	service := strings.TrimSpace(v.config.HysteriaService)
	if service == "" {
		return nil, nil
	}
	output, err := runCommand(ctx, "journalctl", "-u", service, "--since", "2 minutes ago", "--no-pager", "-o", "short-iso")
	if err != nil {
		return nil, err
	}
	events := ParseHysteriaJournal(output)
	out := make([]domain.FlowSnapshot, 0, len(events))
	port := strings.TrimSpace(v.config.HysteriaPort)
	if port == "" {
		port = "3482"
	}
	for _, event := range events {
		key := event.SourceIP + "|" + event.Target + "|" + event.Timestamp
		if _, ok := v.seenHysteria[key]; ok {
			continue
		}
		v.seenHysteria[key] = struct{}{}

		domainName := ""
		ip := ""
		targetHost := extractHost(event.Target)
		if isIPHost(targetHost) {
			ip = targetHost
		} else if isDomainName(targetHost) {
			domainName = targetHost
		}

		out = append(out, domain.FlowSnapshot{
			ID:          "hysteria-" + key,
			Domain:      domainName,
			IP:          ip,
			SourceIP:    event.SourceIP,
			Chains:      []string{"Hysteria-" + port},
			Rule:        categorizeDomain(domainName, "Hysteria"),
			RulePayload: matchedCategoryPayload(domainName),
			Upload:      1,
			Download:    0,
			Connections: 1,
			TimestampMs: nowMs,
		})
	}
	return out, nil
}

func runCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.Output()
}

func positiveDelta(current, previous int64) int64 {
	if current <= previous {
		return 0
	}
	return current - previous
}

func ParseVNStatCounters(data []byte) (map[string]vpsIfaceCounter, error) {
	var payload struct {
		Interfaces []struct {
			Name    string `json:"name"`
			Traffic struct {
				Total struct {
					RX int64 `json:"rx"`
					TX int64 `json:"tx"`
				} `json:"total"`
			} `json:"traffic"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	out := make(map[string]vpsIfaceCounter, len(payload.Interfaces))
	for _, iface := range payload.Interfaces {
		name := strings.TrimSpace(iface.Name)
		if name == "" {
			continue
		}
		out[name] = vpsIfaceCounter{RX: iface.Traffic.Total.RX, TX: iface.Traffic.Total.TX}
	}
	return out, nil
}

type SSEntry struct {
	SourceIP  string
	LocalPort string
}

func ParseSSEstablished(data []byte, ports []string) []SSEntry {
	allowed := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		trimmed := strings.TrimSpace(port)
		if trimmed != "" {
			allowed[trimmed] = struct{}{}
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	var out []SSEntry
	seen := make(map[string]struct{})
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 5 {
			continue
		}
		localHost, localPort := splitAddress(fields[3])
		peerHost, _ := splitAddress(fields[4])
		_ = localHost
		if _, ok := allowed[localPort]; !ok {
			continue
		}
		if net.ParseIP(peerHost) == nil {
			continue
		}
		key := peerHost + ":" + localPort
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, SSEntry{SourceIP: peerHost, LocalPort: localPort})
	}
	return out
}

func splitAddress(value string) (string, string) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") {
		host, port, err := net.SplitHostPort(value)
		if err == nil {
			return strings.Trim(host, "[]"), port
		}
	}
	idx := strings.LastIndex(value, ":")
	if idx < 0 {
		return value, ""
	}
	return strings.Trim(value[:idx], "[]"), value[idx+1:]
}

type HysteriaEvent struct {
	Timestamp string
	SourceIP  string
	Target    string
}

var (
	hysteriaAddrRegex    = regexp.MustCompile(`addr=([0-9a-fA-F:.]+)(?::\d+)?`)
	hysteriaReqAddrRegex = regexp.MustCompile(`reqAddr=([^\s"]+)`)
)

func ParseHysteriaJournal(data []byte) []HysteriaEvent {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var out []HysteriaEvent
	for scanner.Scan() {
		line := scanner.Text()
		addrMatch := hysteriaAddrRegex.FindStringSubmatch(line)
		reqMatch := hysteriaReqAddrRegex.FindStringSubmatch(line)
		if len(addrMatch) < 2 || len(reqMatch) < 2 {
			continue
		}
		timestamp := ""
		if fields := strings.Fields(line); len(fields) > 0 {
			timestamp = fields[0]
		}
		out = append(out, HysteriaEvent{
			Timestamp: timestamp,
			SourceIP:  extractHost(addrMatch[1]),
			Target:    strings.TrimSpace(reqMatch[1]),
		})
	}
	return out
}

var appCategoryRules = map[string][]string{
	"YouTube":   {"youtube.com", "googlevideo.com", "ytimg.com"},
	"Google":    {"google.com", "gstatic.com", "googleapis.com", "googleusercontent.com", "gmail.com", "mtalk.google.com"},
	"Apple":     {"apple.com", "icloud.com", "mzstatic.com", "itunes.apple.com", "push.apple.com"},
	"Telegram":  {"telegram.org", "t.me"},
	"GitHub":    {"github.com", "githubusercontent.com", "githubassets.com"},
	"Microsoft": {"microsoft.com", "live.com", "outlook.com", "hotmail.com", "office.com", "azure.com", "bing.com"},
}

func categorizeDomain(domainName string, fallback string) string {
	if domainName == "" {
		return fallback
	}
	for category, suffixes := range appCategoryRules {
		for _, suffix := range suffixes {
			if domainMatchesSuffix(domainName, suffix) {
				return category
			}
		}
	}
	return fallback
}

func matchedCategoryPayload(domainName string) string {
	if domainName == "" {
		return ""
	}
	for _, suffixes := range appCategoryRules {
		for _, suffix := range suffixes {
			if domainMatchesSuffix(domainName, suffix) {
				return suffix
			}
		}
	}
	return ""
}

func domainMatchesSuffix(domainName, suffix string) bool {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domainName), "."))
	s := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(suffix), "."))
	return d == s || strings.HasSuffix(d, "."+s)
}
