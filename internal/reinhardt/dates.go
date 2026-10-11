package reinhardt

import (
	"fmt"
	"strings"
	"time"
)

func (a *App) displayDate(raw string) string {
	t, e := time.Parse(time.RFC3339Nano, raw)
	if e != nil {
		return raw
	}
	loc, e := time.LoadLocation(a.S.C["TIME_ZONE"])
	if e == nil {
		t = t.In(loc)
	}
	month := int(t.Month()) - 1
	if strings.HasPrefix(a.S.C["LANGUAGE_CODE"], "fr") {
		months := []string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
		return fmt.Sprintf("%d %s %d %02d:%02d", t.Day(), months[month], t.Year(), t.Hour(), t.Minute())
	}
	months := []string{"Jan.", "Feb.", "March", "April", "May", "June", "July", "Aug.", "Sept.", "Oct.", "Nov.", "Dec."}
	hour := t.Hour() % 12
	if hour == 0 {
		hour = 12
	}
	ampm := "a.m."
	if t.Hour() >= 12 {
		ampm = "p.m."
	}
	clock := fmt.Sprintf("%d %s", hour, ampm)
	if t.Minute() != 0 {
		clock = fmt.Sprintf("%d:%02d %s", hour, t.Minute(), ampm)
	} else if t.Hour() == 0 {
		clock = "midnight"
	} else if t.Hour() == 12 {
		clock = "noon"
	}
	return fmt.Sprintf("%s %d, %d, %s", months[month], t.Day(), t.Year(), clock)
}
