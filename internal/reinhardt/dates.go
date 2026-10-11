package reinhardt

import (
	"fmt"
	"strings"
	"time"
)

func (app *App) displayDate(raw string) string {
	parsedTime, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	loc, err := time.LoadLocation(app.Store.Config["TIME_ZONE"])
	if err == nil {
		parsedTime = parsedTime.In(loc)
	}
	month := int(parsedTime.Month()) - 1
	if strings.HasPrefix(app.Store.Config["LANGUAGE_CODE"], "fr") {
		months := []string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}
		return fmt.Sprintf("%d %s %d %02d:%02d", parsedTime.Day(), months[month], parsedTime.Year(), parsedTime.Hour(), parsedTime.Minute())
	}
	months := []string{"Jan.", "Feb.", "March", "April", "May", "June", "July", "Aug.", "Sept.", "Oct.", "Nov.", "Dec."}
	hour := parsedTime.Hour() % 12
	if hour == 0 {
		hour = 12
	}
	ampm := "a.m."
	if parsedTime.Hour() >= 12 {
		ampm = "p.m."
	}
	clock := fmt.Sprintf("%d %s", hour, ampm)
	if parsedTime.Minute() != 0 {
		clock = fmt.Sprintf("%d:%02d %s", hour, parsedTime.Minute(), ampm)
	} else if parsedTime.Hour() == 0 {
		clock = "midnight"
	} else if parsedTime.Hour() == 12 {
		clock = "noon"
	}
	return fmt.Sprintf("%s %d, %d, %s", months[month], parsedTime.Day(), parsedTime.Year(), clock)
}
