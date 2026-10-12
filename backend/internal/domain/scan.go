package domain

// ScanResult is the outcome of one scan at a gate, as a device shows it and as the server
// stores it (scan_logs.result).
type ScanResult string

const (
	ScanAdmitted   ScanResult = "ADMITTED"
	ScanDuplicate  ScanResult = "DUPLICATE"
	ScanInvalid    ScanResult = "INVALID"
	ScanWrongEvent ScanResult = "WRONG_EVENT"
	ScanWrongGate  ScanResult = "WRONG_GATE"
)

// ScanResults lists every result, in the order of the contract's enum.
var ScanResults = []ScanResult{ScanAdmitted, ScanDuplicate, ScanInvalid, ScanWrongEvent, ScanWrongGate}

// Valid reports whether r is one of the five results.
func (r ScanResult) Valid() bool {
	for _, v := range ScanResults {
		if r == v {
			return true
		}
	}
	return false
}

// MaxScansPerBatch is the most scans one upload may carry (the contract's maxItems).
const MaxScansPerBatch = 500

// ManifestCode is the one-letter status of a ticket in the gate manifest: V valid, U used,
// X void, T transferred. An unknown status is X: a device must refuse what it cannot read.
func (s TicketStatus) ManifestCode() byte {
	switch s {
	case TicketValid:
		return 'V'
	case TicketUsed:
		return 'U'
	case TicketTransferred:
		return 'T'
	default:
		return 'X'
	}
}

// AdmitsAtGate reports whether a ticket in this status can still be the subject of an
// admission: VALID (nobody has entered on it) or USED (somebody has, and a scan is judged
// against that entry). VOID and TRANSFERRED tickets admit nobody.
func (s TicketStatus) AdmitsAtGate() bool { return s == TicketValid || s == TicketUsed }
