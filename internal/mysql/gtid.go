package mysql

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Interval represents a continuous range of GTID transaction sequence numbers [Start, End].
type Interval struct {
	Start int64
	End   int64
}

// Contains checks if the given sequence number is inside this interval.
func (i Interval) Contains(seq int64) bool {
	return seq >= i.Start && seq <= i.End
}

// Count returns the number of transactions in this interval.
func (i Interval) Count() int64 {
	if i.End < i.Start {
		return 0
	}
	return i.End - i.Start + 1
}

// UUIDSet represents a collection of intervals for a specific MySQL server UUID.
type UUIDSet struct {
	UUID      string
	Intervals []Interval
}

// TotalTransactions returns the total count of transactions for this UUID.
func (u *UUIDSet) TotalTransactions() int64 {
	var count int64
	for _, iv := range u.Intervals {
		count += iv.Count()
	}
	return count
}

// Contains checks if this UUIDSet contains all transactions from another UUIDSet.
func (u *UUIDSet) Contains(other *UUIDSet) bool {
	if other == nil || len(other.Intervals) == 0 {
		return true
	}
	for _, oiv := range other.Intervals {
		for seq := oiv.Start; seq <= oiv.End; seq++ {
			found := false
			for _, miv := range u.Intervals {
				if miv.Contains(seq) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}

// GTIDSet represents a parsed MySQL GTID set (e.g. "uuid1:1-100,uuid2:1-50").
type GTIDSet struct {
	Sets map[string]*UUIDSet
}

// NewGTIDSet initializes an empty GTIDSet.
func NewGTIDSet() *GTIDSet {
	return &GTIDSet{
		Sets: make(map[string]*UUIDSet),
	}
}

// ParseGTIDSet parses a MySQL GTID set string.
func ParseGTIDSet(raw string) (*GTIDSet, error) {
	gtidSet := NewGTIDSet()
	cleaned := strings.TrimSpace(raw)
	// Remove newlines and quotes
	cleaned = strings.ReplaceAll(cleaned, "\n", "")
	cleaned = strings.ReplaceAll(cleaned, "\r", "")
	cleaned = strings.ReplaceAll(cleaned, "'", "")
	cleaned = strings.ReplaceAll(cleaned, " ", "")

	if cleaned == "" {
		return gtidSet, nil
	}

	parts := strings.Split(cleaned, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		colonIdx := strings.Index(part, ":")
		if colonIdx <= 0 {
			return nil, fmt.Errorf("invalid GTID set fragment (missing colon): %s", part)
		}
		uuid := strings.ToLower(part[:colonIdx])
		intervalsStr := part[colonIdx+1:]

		uuidSet, exists := gtidSet.Sets[uuid]
		if !exists {
			uuidSet = &UUIDSet{UUID: uuid}
			gtidSet.Sets[uuid] = uuidSet
		}

		ivTokens := strings.Split(intervalsStr, ":")
		for _, token := range ivTokens {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if strings.Contains(token, "-") {
				rangeParts := strings.Split(token, "-")
				if len(rangeParts) != 2 {
					return nil, fmt.Errorf("invalid GTID interval: %s", token)
				}
				start, err1 := strconv.ParseInt(rangeParts[0], 10, 64)
				end, err2 := strconv.ParseInt(rangeParts[1], 10, 64)
				if err1 != nil || err2 != nil {
					return nil, fmt.Errorf("invalid GTID range numbers: %s", token)
				}
				uuidSet.Intervals = append(uuidSet.Intervals, Interval{Start: start, End: end})
			} else {
				seq, err := strconv.ParseInt(token, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid single GTID number: %s", token)
				}
				uuidSet.Intervals = append(uuidSet.Intervals, Interval{Start: seq, End: seq})
			}
		}
	}

	return gtidSet, nil
}

// TotalTransactions calculates the sum of all transactions across all UUIDs.
func (g *GTIDSet) TotalTransactions() int64 {
	if g == nil {
		return 0
	}
	var total int64
	for _, u := range g.Sets {
		total += u.TotalTransactions()
	}
	return total
}

// Contains checks if GTIDSet 'g' contains all transactions present in 'other'.
func (g *GTIDSet) Contains(other *GTIDSet) bool {
	if other == nil || len(other.Sets) == 0 {
		return true
	}
	if g == nil || len(g.Sets) == 0 {
		return false
	}
	for uuid, otherUUIDSet := range other.Sets {
		myUUIDSet, exists := g.Sets[uuid]
		if !exists {
			return false
		}
		if !myUUIDSet.Contains(otherUUIDSet) {
			return false
		}
	}
	return true
}

// String returns a normalized string representation of the GTIDSet.
func (g *GTIDSet) String() string {
	if g == nil || len(g.Sets) == 0 {
		return ""
	}
	var uuids []string
	for u := range g.Sets {
		uuids = append(uuids, u)
	}
	sort.Strings(uuids)

	var fragments []string
	for _, u := range uuids {
		uset := g.Sets[u]
		var ivStrs []string
		for _, iv := range uset.Intervals {
			if iv.Start == iv.End {
				ivStrs = append(ivStrs, fmt.Sprintf("%d", iv.Start))
			} else {
				ivStrs = append(ivStrs, fmt.Sprintf("%d-%d", iv.Start, iv.End))
			}
		}
		fragments = append(fragments, fmt.Sprintf("%s:%s", u, strings.Join(ivStrs, ":")))
	}
	return strings.Join(fragments, ",")
}

// CompareGTIDSets determines which GTIDSet is more advanced.
// Returns:
//   1 if a is ahead of b (a contains b, or a has more transactions)
//  -1 if b is ahead of a (b contains a, or b has more transactions)
//   0 if they are identical
func CompareGTIDSets(a, b *GTIDSet) int {
	aNil := a == nil || len(a.Sets) == 0
	bNil := b == nil || len(b.Sets) == 0

	if aNil && bNil {
		return 0
	}
	if aNil {
		return -1
	}
	if bNil {
		return 1
	}

	aContainsB := a.Contains(b)
	bContainsA := b.Contains(a)

	if aContainsB && bContainsA {
		return 0
	}
	if aContainsB && !bContainsA {
		return 1
	}
	if !aContainsB && bContainsA {
		return -1
	}

	// Incomparable/divergent sets: use total transaction count as tiebreaker
	countA := a.TotalTransactions()
	countB := b.TotalTransactions()
	if countA > countB {
		return 1
	} else if countB > countA {
		return -1
	}
	return 0
}
