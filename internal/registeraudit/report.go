package registeraudit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// CloudDevice is one row of the OPTIONAL cloud device list the operator saves
// to a local JSON file (the audit itself makes no network call). Keys, tokens
// and bearer hashes are never part of it; a key_fingerprint is printed as 8
// hex characters.
type CloudDevice struct {
	DeviceID           string `json:"device_id"`
	TillID             string `json:"till_id"`
	Role               string `json:"role"`
	LastUsedAt         string `json:"last_used_at"`
	RevokedReason      string `json:"revoked_reason"`
	KeyFingerprint     string `json:"key_fingerprint"`
	ReplacedByDeviceID string `json:"replaced_by_device_id"`
}

// LoadCloudDevices reads the optional cloud device list file.
func LoadCloudDevices(path string) ([]CloudDevice, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cloud devices: %w", err)
	}
	var out []CloudDevice
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse cloud devices: %w", err)
	}
	return out, nil
}

// fingerprint8 is 8 hex characters: kept when the value already is one,
// otherwise the first 8 hex of its sha256, so a raw key never reaches output.
func fingerprint8(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "none"
	}
	if len(v) == 8 {
		if _, err := hex.DecodeString(v); err == nil {
			return v
		}
	}
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])[:8]
}

// Row is one register in the audit output (BA section 6 fields).
type Row struct {
	RegisterID     string `json:"register_id"`
	Name           string `json:"name"`
	IsActive       bool   `json:"is_active"`
	Location       string `json:"location"`
	TillID         string `json:"till_id"`
	TillName       string `json:"till_name"`
	TillRole       string `json:"till_role"`
	DeviceID       string `json:"device_id"`
	KeyFingerprint string `json:"key_fingerprint"`
	LastSeen       string `json:"last_seen"`
	EnrolledAt     string `json:"enrolled_at"`
	SalesCount     int    `json:"sales_count"`
	LastSaleAt     string `json:"last_sale_at"`
	OpenShift      bool   `json:"open_shift"`
	UnsentSales    int    `json:"unsent_sales"`
	InCloud        string `json:"in_cloud"`    // yes | no | unknown
	CloudState     string `json:"cloud_state"` // revoked reason, role, or unknown
	State          string `json:"state"`
	SupersededBy   string `json:"superseded_by"`
	RetireSafe     bool   `json:"retire_safe"`
	Evidence       string `json:"evidence"`
}

// Comparison lists ids present in one of the three lists but not the others.
type Comparison struct {
	RegistersWithoutTill []string `json:"registers_without_till"`
	TillsWithoutRegister []string `json:"tills_without_register"`
	CloudWithoutRegister []string `json:"cloud_devices_without_register"`
	CloudListProvided    bool     `json:"cloud_list_provided"`
	LinkTableSchema      string   `json:"link_table"` // present | absent (schema 119 or older)
}

// Report is the whole audit.
type Report struct {
	Build      string         `json:"build"`
	QuietDays  int            `json:"quiet_days"`
	Counts     map[string]int `json:"counts"`
	Comparison Comparison     `json:"comparison"`
	Rows       []Row          `json:"rows"`
}

// Options of a Build.
type Options struct {
	Now       time.Time
	QuietDays int
	Cloud     []CloudDevice // nil = no cloud list
	Build     string
}

func tstr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Build reads the facts through q (read-only) and classifies every register.
func Build(ctx context.Context, q data.AuditQuerier, o Options) (Report, error) {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.QuietDays <= 0 {
		o.QuietDays = DefaultQuietDays
	}
	regs, err := data.AuditRegisters(ctx, q)
	if err != nil {
		return Report{}, err
	}
	tillRows, err := data.AuditTills(ctx, q)
	if err != nil {
		return Report{}, err
	}
	hasLink, err := data.HasRegisterDevices(ctx, q)
	if err != nil {
		return Report{}, err
	}
	own := data.AuditOwnRegisterID(ctx, q)
	cloudProvided := o.Cloud != nil
	byDev := map[string]CloudDevice{}
	byTill := map[string]CloudDevice{}
	for _, c := range o.Cloud {
		if c.DeviceID != "" {
			byDev[c.DeviceID] = c
		}
		if c.TillID != "" {
			byTill[c.TillID] = c
		}
	}

	active := 0
	for i := range regs {
		regs[i].IsSelf = regs[i].RegisterID == own
		if regs[i].IsActive {
			active++
		}
	}
	// Fingerprint -> registers of that key, to spot a device that came back
	// under a new device id (same key, newer register).
	fpOwner := map[string][]int{}
	for i, r := range regs {
		if c, ok := lookupCloud(byDev, byTill, r); ok && c.KeyFingerprint != "" {
			fp := fingerprint8(c.KeyFingerprint)
			fpOwner[fp] = append(fpOwner[fp], i)
		}
	}

	rep := Report{Build: o.Build, QuietDays: o.QuietDays, Counts: map[string]int{}}
	// Empty lists print as [] in JSON (not null), matching the table.
	rep.Comparison.RegistersWithoutTill = []string{}
	rep.Comparison.TillsWithoutRegister = []string{}
	rep.Comparison.CloudWithoutRegister = []string{}
	rep.Comparison.CloudListProvided = cloudProvided
	rep.Comparison.LinkTableSchema = "present"
	if !hasLink {
		rep.Comparison.LinkTableSchema = "absent (schema 119 or older: link taken from the enrolment audit row)"
	}
	seenTill := map[string]bool{}
	for i, r := range regs {
		c, inCloud := lookupCloud(byDev, byTill, r)
		gone := cloudProvided && (r.TillExists && !inCloud && !r.TillLastSeen.IsZero() && o.Now.Sub(r.TillLastSeen) > 30*24*time.Hour ||
			inCloud && c.RevokedReason != "")
		if cloudProvided && inCloud && c.ReplacedByDeviceID != "" && r.SupersededBy == "" {
			r.SupersededBy = c.ReplacedByDeviceID
			gone = true
		}
		if r.SupersededBy == "" && inCloud && c.KeyFingerprint != "" {
			fp := fingerprint8(c.KeyFingerprint)
			for _, j := range fpOwner[fp] {
				if j != i && regs[j].IsActive && regs[j].TillEnrolledAt.After(r.TillEnrolledAt) && regs[j].DeviceID != r.DeviceID {
					r.SupersededBy = regs[j].DeviceID
					break
				}
			}
		}
		res := Classify(r, Context{Now: o.Now, QuietDays: o.QuietDays, ActiveCount: active, TillGone: gone})
		row := Row{
			RegisterID: r.RegisterID, Name: r.Name, IsActive: r.IsActive,
			Location: r.LocationName, TillID: r.TillID, TillName: r.TillName, TillRole: r.TillRole,
			DeviceID: r.DeviceID, KeyFingerprint: "unknown",
			LastSeen: tstr(r.TillLastSeen), EnrolledAt: tstr(r.TillEnrolledAt),
			SalesCount: r.SalesCount, LastSaleAt: tstr(r.LastSale), OpenShift: r.OpenShift, UnsentSales: r.UnsentSales,
			InCloud: "unknown", CloudState: "unknown",
			State: res.AuditState(), SupersededBy: r.SupersededBy, RetireSafe: res.RetireSafe, Evidence: res.Evidence,
		}
		if row.Location == "" {
			row.Location = "Unassigned (sells from the main location)"
		}
		if row.DeviceID == "" {
			row.DeviceID = "unknown"
		}
		if r.IsSelf {
			row.TillID, row.TillName, row.TillRole = "none", "this till", "main"
		}
		if cloudProvided {
			row.InCloud, row.CloudState = "no", "not in the account list"
			if inCloud {
				row.InCloud = "yes"
				row.KeyFingerprint = fingerprint8(c.KeyFingerprint)
				row.CloudState = c.Role
				if c.RevokedReason != "" {
					row.CloudState = "revoked: " + c.RevokedReason
				}
				if row.DeviceID == "unknown" && c.DeviceID != "" {
					row.DeviceID = c.DeviceID
				}
			}
		}
		if r.TillID != "" {
			seenTill[r.TillID] = true
			if !r.TillExists && !r.IsSelf {
				rep.Comparison.RegistersWithoutTill = append(rep.Comparison.RegistersWithoutTill, r.RegisterID)
			}
		} else if !r.IsSelf {
			rep.Comparison.RegistersWithoutTill = append(rep.Comparison.RegistersWithoutTill, r.RegisterID)
		}
		rep.Counts[row.State]++
		rep.Rows = append(rep.Rows, row)
	}
	for _, t := range tillRows {
		if !seenTill[t.ID] {
			rep.Comparison.TillsWithoutRegister = append(rep.Comparison.TillsWithoutRegister, t.ID)
		}
	}
	if cloudProvided {
		knownDev, knownTill := map[string]bool{}, map[string]bool{}
		for _, r := range regs {
			if r.DeviceID != "" {
				knownDev[r.DeviceID] = true
			}
			if r.TillID != "" {
				knownTill[r.TillID] = true
			}
		}
		for _, c := range o.Cloud {
			// The main till (this one) has no register link by design: not a gap.
			if c.Role == "main" {
				continue
			}
			if !knownDev[c.DeviceID] && !knownTill[c.TillID] {
				rep.Comparison.CloudWithoutRegister = append(rep.Comparison.CloudWithoutRegister, firstNonEmpty(c.TillID, c.DeviceID))
			}
		}
	}
	sort.SliceStable(rep.Rows, func(i, j int) bool { return rep.Rows[i].LastSeen > rep.Rows[j].LastSeen })
	return rep, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func lookupCloud(byDev, byTill map[string]CloudDevice, r data.RegisterAuditRow) (CloudDevice, bool) {
	if r.DeviceID != "" {
		if c, ok := byDev[r.DeviceID]; ok {
			return c, true
		}
	}
	if r.TillID != "" {
		if c, ok := byTill[r.TillID]; ok {
			return c, true
		}
	}
	return CloudDevice{}, false
}

// WriteJSON prints the report as indented JSON.
func WriteJSON(w io.Writer, rep Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}

// WriteTable prints the report as a text table with a header.
func WriteTable(w io.Writer, rep Report) error {
	out := w // operator CLI text, not an HTTP response
	fmt.Fprintf(out, "register-audit (read-only)  build: %s  quiet days: %d\n", rep.Build, rep.QuietDays)
	var keys []string
	for k := range rep.Counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprint(out, "counts:")
	for _, k := range keys {
		fmt.Fprintf(out, " %s=%d", k, rep.Counts[k])
	}
	fmt.Fprintf(out, "\nlink table: %s\n", rep.Comparison.LinkTableSchema)
	fmt.Fprintf(out, "registers without a till row: %v\ntills without a register: %v\n", rep.Comparison.RegistersWithoutTill, rep.Comparison.TillsWithoutRegister)
	if rep.Comparison.CloudListProvided {
		fmt.Fprintf(out, "cloud devices without a register: %v\n", rep.Comparison.CloudWithoutRegister)
	} else {
		fmt.Fprintln(out, "cloud devices: no list given (in_cloud and key_fingerprint print unknown)")
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "REGISTER\tNAME\tACTIVE\tSTATE\tRETIRE_SAFE\tTILL\tDEVICE\tKEY\tSALES\tLAST_SALE\tUNSENT\tIN_CLOUD\tEVIDENCE")
	for _, r := range rep.Rows {
		fmt.Fprintf(tw, "%s\t%s\t%v\t%s\t%v\t%s\t%s\t%s\t%d\t%s\t%d\t%s\t%s\n",
			r.RegisterID, r.Name, r.IsActive, r.State, r.RetireSafe, r.TillName, r.DeviceID, r.KeyFingerprint,
			r.SalesCount, r.LastSaleAt, r.UnsentSales, r.InCloud, r.Evidence)
	}
	return tw.Flush()
}
