package xray

// ClientTraffic represents traffic statistics and limits for a specific client.
// It tracks upload/download usage, expiry times, and online status for inbound clients.
type ClientTraffic struct {
	Id        int `json:"id" form:"id" gorm:"primaryKey;autoIncrement"`
	InboundId int `json:"inboundId" form:"inboundId" gorm:"index"`
	// Enable+LastOnline back the "seen recently" query the traffic job runs
	// every 10s; without the composite index it scanned the whole table.
	Enable     bool   `json:"enable" form:"enable" gorm:"index:idx_ct_enable_last_online,priority:1"`
	Email      string `json:"email" form:"email" gorm:"unique"`
	UUID       string `json:"uuid" form:"uuid" gorm:"-"`
	SubId      string `json:"subId" form:"subId" gorm:"-"`
	Up         int64  `json:"up" form:"up"`
	Down       int64  `json:"down" form:"down"`
	AllTime    int64  `json:"allTime" form:"allTime"`
	ExpiryTime int64  `json:"expiryTime" form:"expiryTime"`
	Total      int64  `json:"total" form:"total"`
	Reset      int    `json:"reset" form:"reset" gorm:"default:0"`
	LastOnline int64  `json:"lastOnline" form:"lastOnline" gorm:"default:0;index:idx_ct_enable_last_online,priority:2"`
}
