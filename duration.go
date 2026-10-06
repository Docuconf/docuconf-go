package docuconf

import (
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parseDuration parses a duration in the variable's encoding (SPEC §5).
func (v *varDecl) parseDuration(raw string) (time.Duration, error) {
	switch v.durEncoding {
	case encISO8601:
		return parseISO8601(raw)
	case encSeconds:
		return parseSeconds(raw)
	case encTimespan:
		return parseTimespan(raw)
	}
	return time.ParseDuration(raw)
}

// durationHint shows what a duration looks like in an encoding.
func durationHint(encoding string) string {
	switch encoding {
	case encISO8601:
		return "in ISO 8601 form such as PT90S"
	case encSeconds:
		return "in seconds such as 90 or 1.5"
	case encTimespan:
		return "of the form [d.]hh:mm:ss[.fff] such as 00:01:30"
	}
	return "such as 1m30s"
}

var (
	errBadDuration = errors.New("invalid duration")

	isoNum      = `([0-9]+(?:[.,][0-9]+)?)`
	iso8601Re   = regexp.MustCompile(`^P(?:` + isoNum + `D)?(?:T(?:` + isoNum + `H)?(?:` + isoNum + `M)?(?:` + isoNum + `S)?)?$`)
	secondsRe   = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
	timespanRe  = regexp.MustCompile(`^(?:([0-9]+)\.)?([0-9]{1,2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,7}))?$`)
	maxDuration = new(big.Rat).SetInt64(math.MaxInt64)
)

// durationSum adds decimal amounts of units exactly, truncates to whole
// nanoseconds and fails beyond time.Duration's range.
type durationSum struct{ r big.Rat }

func (s *durationSum) add(decimal string, unit time.Duration) bool {
	if decimal == "" {
		return true
	}
	x, ok := new(big.Rat).SetString(strings.Replace(decimal, ",", ".", 1))
	if !ok {
		return false
	}
	s.r.Add(&s.r, x.Mul(x, new(big.Rat).SetInt64(int64(unit))))
	return true
}

func (s *durationSum) value() (time.Duration, error) {
	if s.r.Cmp(maxDuration) > 0 {
		return 0, errBadDuration
	}
	n := new(big.Int).Quo(s.r.Num(), s.r.Denom())
	return time.Duration(n.Int64()), nil
}

// parseISO8601 parses an ISO 8601 duration of days, hours, minutes and
// seconds, such as PT90S, PT1.5S or P1DT2H. Years, months and weeks have
// no fixed length and are rejected.
func parseISO8601(raw string) (time.Duration, error) {
	m := iso8601Re.FindStringSubmatch(raw)
	if m == nil || raw == "P" || strings.HasSuffix(raw, "T") {
		return 0, errBadDuration
	}
	var s durationSum
	for i, unit := range []time.Duration{24 * time.Hour, time.Hour, time.Minute, time.Second} {
		if !s.add(m[i+1], unit) {
			return 0, errBadDuration
		}
	}
	return s.value()
}

// parseSeconds parses a decimal number of seconds, such as 90 or 0.25.
func parseSeconds(raw string) (time.Duration, error) {
	var s durationSum
	if !secondsRe.MatchString(raw) || !s.add(raw, time.Second) {
		return 0, errBadDuration
	}
	return s.value()
}

// parseTimespan parses .NET TimeSpan's constant format,
// [d.]hh:mm:ss[.fffffff], with hours below 24 and minutes and seconds
// below 60.
func parseTimespan(raw string) (time.Duration, error) {
	m := timespanRe.FindStringSubmatch(raw)
	if m == nil {
		return 0, errBadDuration
	}
	hh, _ := strconv.Atoi(m[2])
	mm, _ := strconv.Atoi(m[3])
	ss, _ := strconv.Atoi(m[4])
	if hh > 23 || mm > 59 || ss > 59 {
		return 0, errBadDuration
	}
	secs := m[4]
	if m[5] != "" {
		secs += "." + m[5]
	}
	var s durationSum
	s.add(m[1], 24*time.Hour)
	s.add(m[2], time.Hour)
	s.add(m[3], time.Minute)
	s.add(secs, time.Second)
	return s.value()
}
