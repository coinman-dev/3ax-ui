package model

// LinkPort is the port a client link must carry: the nginx front-end (3AX-UI)
// moves an inbound to a loopback port while clients keep dialling PublicPort.
func (i *Inbound) LinkPort() int {
	if i.PublicPort > 0 {
		return i.PublicPort
	}
	return i.Port
}

// StubSite is a cover page the nginx front-end serves on the panel's domain,
// kept in the database so it rides along in backups; only the active one is on disk.
type StubSite struct {
	Id        int    `json:"id" gorm:"primaryKey;autoIncrement"`
	Name      string `json:"name"`
	Html      string `json:"html"`
	Active    bool   `json:"active"`
	Size      int    `json:"size"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}
