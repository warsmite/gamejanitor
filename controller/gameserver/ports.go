package gameserver

func ptrIntOr0(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}

