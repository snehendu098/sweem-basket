package chains

import "strings"

const (
	BaseMainnet = 8453
	BaseSepolia = 84532
)

const (
	LabelBaseMainnet = "base"
	LabelBaseSepolia = "base-sepolia"
)

var byLabel = map[string]int{
	LabelBaseMainnet: BaseMainnet,
	LabelBaseSepolia: BaseSepolia,
}

var byID = map[int]string{
	BaseMainnet: LabelBaseMainnet,
	BaseSepolia: LabelBaseSepolia,
}

func Supported() []int { return []int{BaseMainnet, BaseSepolia} }

func ID(label string) (int, bool) {
	id, ok := byLabel[strings.ToLower(strings.TrimSpace(label))]
	return id, ok
}

func Label(id int) (string, bool) {
	l, ok := byID[id]
	return l, ok
}

func Normalize(label string) (string, bool) {
	id, ok := ID(label)
	if !ok {
		return "", false
	}
	return byID[id], true
}
