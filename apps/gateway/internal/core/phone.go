package core

import "strings"

const (
	visiblePhonePrefix = 4
	visiblePhoneSuffix = 2
)

func MaskPhone(phone string) string {
	if len(phone) <= visiblePhonePrefix+visiblePhoneSuffix {
		return strings.Repeat("*", len(phone))
	}
	hidden := len(phone) - visiblePhonePrefix - visiblePhoneSuffix
	return phone[:visiblePhonePrefix] + strings.Repeat("*", hidden) + phone[len(phone)-visiblePhoneSuffix:]
}
