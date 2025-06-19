package server

import (
	"strings"
	"time"

	"github.com/mimecast/dtail/internal/constants"
)

func fillDates(str string) string {
	yyyesterday := time.Now().Add(-3 * constants.DayDuration).Format("20060102")
	str = strings.ReplaceAll(str, "$yyyesterday", yyyesterday)

	yyesterday := time.Now().Add(-2 * constants.DayDuration).Format("20060102")
	str = strings.ReplaceAll(str, "$yyesterday", yyesterday)

	yesterday := time.Now().Add(-1 * constants.DayDuration).Format("20060102")
	str = strings.ReplaceAll(str, "$yesterday", yesterday)

	today := time.Now().Format("20060102")
	str = strings.ReplaceAll(str, "$today", today)

	tomorrow := time.Now().Add(1 * constants.DayDuration).Format("20060102")
	return strings.ReplaceAll(str, "$tomorrow", tomorrow)
}
