package reinhardt

import (
	"fmt"
	"time"
)

const databaseTimestampLayout = "2006-01-02 15:04:05.000000"

func (translator *Translator) displayDate(raw, timezone string) string {
	parsedTime, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	loc, err := time.LoadLocation(timezone)
	if err == nil {
		parsedTime = parsedTime.In(loc)
	}
	hour := parsedTime.Hour() % 12
	if hour == 0 {
		hour = 12
	}
	period := translator.Text("date.am")
	if parsedTime.Hour() >= 12 {
		period = translator.Text("date.pm")
	}
	data := map[string]any{
		"Day": parsedTime.Day(), "Month": translator.Text(fmt.Sprintf("date.month.%d", parsedTime.Month())),
		"Year": parsedTime.Year(), "Hour": parsedTime.Hour(), "Hour12": hour,
		"Minute": parsedTime.Minute(), "Period": period,
	}
	clock, err := translator.Format("date.clock", data)
	if err != nil {
		return raw
	}
	if parsedTime.Minute() == 0 {
		if parsedTime.Hour() == 0 {
			clock = translator.Text("date.clock.midnight")
		} else if parsedTime.Hour() == 12 {
			clock = translator.Text("date.clock.noon")
		}
	}
	data["Clock"] = clock
	formatted, err := translator.Format("date.format", data)
	if err != nil {
		return raw
	}
	return formatted
}
