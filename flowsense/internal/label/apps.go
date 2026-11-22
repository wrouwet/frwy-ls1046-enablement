package label

import (
	"net/netip"
	"strings"
)

// App describes one recognisable application.
type App struct {
	Name     string
	Category string
	Domains  []string       // suffix match on SNI/Host/DNS names
	Prefixes []netip.Prefix // server address ranges operated by the app (game servers rarely have names)
	UDPPorts [][2]uint16    // server UDP port ranges typical for the app's realtime traffic
}

// Categories.
const (
	CatGaming  = "Gaming"
	CatVideo   = "Video"
	CatMusic   = "Music"
	CatSocial  = "Social"
	CatChat    = "Chat & calls"
	CatAdult   = "Adult"
	CatUpdates = "Updates & cloud"
	CatDev     = "AI & dev"
	CatAds     = "Ads & tracking"
	CatWeb     = "Web"
	CatOther   = "Other"
)

// Generic app names for traffic without a specific app.
const (
	AppWeb     = "Web browsing"
	AppUnknown = "Unknown"
	AppDNS     = "DNS"
	AppVPN     = "VPN"
)

func pfx(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// Apps is the rule table. Address ranges are the operators' published/ASN
// ranges at the time of writing (Roblox AS22697, Riot AS6507, Valve AS32590)
// and may drift; names are the primary signal.
var Apps = []App{
	{Name: "YouTube", Category: CatVideo, Domains: []string{"youtube.com", "googlevideo.com", "ytimg.com", "youtu.be", "youtube-nocookie.com", "youtubei.googleapis.com", "yt3.ggpht.com"}},
	{Name: "Netflix", Category: CatVideo, Domains: []string{"netflix.com", "nflxvideo.net", "nflxso.net", "nflxext.com", "nflximg.net"}},
	{Name: "Twitch", Category: CatVideo, Domains: []string{"twitch.tv", "ttvnw.net", "jtvnw.net", "twitchcdn.net", "twitchsvc.net"}},
	{Name: "Prime Video", Category: CatVideo, Domains: []string{"primevideo.com", "aiv-cdn.net", "aiv-delivery.net", "amazonvideo.com"}},
	{Name: "Disney+", Category: CatVideo, Domains: []string{"disneyplus.com", "dssott.com", "bamgrid.com", "disney-plus.net"}},
	{Name: "Max / Hulu", Category: CatVideo, Domains: []string{"hbomax.com", "max.com", "hulu.com", "hulustream.com", "huluim.com"}},
	{Name: "Vimeo", Category: CatVideo, Domains: []string{"vimeo.com", "vimeocdn.com", "livestream.com"}},
	{Name: "Plex", Category: CatVideo, Domains: []string{"plex.tv", "plex.direct"}},
	{Name: "Spotify", Category: CatMusic, Domains: []string{"spotify.com", "scdn.co", "spotifycdn.com", "spotifycdn.net", "spotify.design"}},
	{Name: "SoundCloud", Category: CatMusic, Domains: []string{"soundcloud.com", "sndcdn.com", "soundcloud.cloud"}},
	{Name: "Discord", Category: CatChat, Domains: []string{"discord.com", "discord.gg", "discordapp.com", "discordapp.net", "discord.media", "discordcdn.com", "discordstatus.com"},
		UDPPorts: [][2]uint16{{50000, 65535}}},
	{Name: "WhatsApp", Category: CatChat, Domains: []string{"whatsapp.net", "whatsapp.com"}},
	{Name: "Zoom / Teams", Category: CatChat, Domains: []string{"zoom.us", "zoom.com", "teams.microsoft.com", "teams.live.com", "skype.com", "webex.com"}},
	{Name: "Xbox Live", Category: CatGaming, Domains: []string{"xboxlive.com", "xbox.com", "xboxservices.com", "playfabapi.com", "playfab.com", "gamepass.com", "xboxab.com", "xsts.auth.xboxlive.com"},
		UDPPorts: [][2]uint16{{3074, 3074}, {3544, 3544}}},
	{Name: "PlayStation Network", Category: CatGaming, Domains: []string{"playstation.net", "playstation.com", "sonyentertainmentnetwork.com", "psn.com"}},
	{Name: "Valorant / Riot", Category: CatGaming, Domains: []string{"riotgames.com", "riotcdn.net", "playvalorant.com", "leagueoflegends.com", "pvp.net", "riotclient.com"},
		Prefixes: pfx("104.160.128.0/19", "162.249.72.0/21", "185.40.64.0/22", "192.64.168.0/21"), UDPPorts: [][2]uint16{{7000, 8000}}},
	{Name: "Roblox", Category: CatGaming, Domains: []string{"roblox.com", "rbxcdn.com", "rbx.com", "robloxlabs.com", "roblox.qq.com"},
		Prefixes: pfx("128.116.0.0/17")},
	{Name: "Steam", Category: CatGaming, Domains: []string{"steampowered.com", "steamcommunity.com", "steamcontent.com", "steamserver.net", "steamstatic.com", "valve.net", "steam-chat.com"},
		Prefixes: pfx("155.133.224.0/19", "162.254.192.0/21", "185.25.180.0/22", "208.64.200.0/22", "146.66.152.0/21"), UDPPorts: [][2]uint16{{27000, 27100}}},
	{Name: "Fortnite / Epic", Category: CatGaming, Domains: []string{"epicgames.com", "epicgames.dev", "unrealengine.com", "fortnite.com", "easyanticheat.net"}},
	{Name: "Minecraft", Category: CatGaming, Domains: []string{"minecraft.net", "mojang.com", "minecraftservices.com"}},
	{Name: "Battle.net / Activision", Category: CatGaming, Domains: []string{"battle.net", "blizzard.com", "activision.com", "callofduty.com", "demonware.net"}},
	{Name: "EA", Category: CatGaming, Domains: []string{"ea.com", "origin.com", "eaplay.com"}},
	{Name: "Mobile games", Category: CatGaming, Domains: []string{"king.com", "midasplayer.net", "unity3d.com", "unitychina.cn", "gamedock.io", "easybrain.com", "supercell.com", "supercell.net"}},
	{Name: "Adult sites", Category: CatAdult, Domains: []string{"pornhub.com", "phncdn.com", "pornhubpremium.com", "xvideos.com", "xvideos-cdn.com", "xnxx.com", "xnxx-cdn.com",
		"xhamster.com", "xhcdn.com", "xhamsterlive.com", "redtube.com", "rdtcdn.com", "youporn.com", "ypncdn.com", "tube8.com", "spankbang.com", "eporner.com",
		"onlyfans.com", "chaturbate.com", "highwebmedia.com", "stripchat.com", "bongacams.com", "cam4.com", "livejasmin.com", "brazzers.com", "motherless.com",
		"txxx.com", "hqporner.com", "porntrex.com", "beeg.com", "4tube.com", "youjizz.com", "fansly.com", "manyvids.com", "myfreecams.com", "camsoda.com",
		"xxx", "porn", "sex", "adult"}},
	{Name: "TikTok", Category: CatSocial, Domains: []string{"tiktok.com", "tiktokcdn.com", "tiktokv.com", "tiktokcdn-us.com", "byteoversea.com", "ibytedtos.com", "musical.ly"}},
	{Name: "Instagram", Category: CatSocial, Domains: []string{"instagram.com", "cdninstagram.com"}},
	{Name: "Facebook", Category: CatSocial, Domains: []string{"facebook.com", "fbcdn.net", "facebook.net", "messenger.com", "fbsbx.com"}},
	{Name: "Snapchat", Category: CatSocial, Domains: []string{"snapchat.com", "sc-cdn.net", "snapkit.com", "snap-dev.net"}},
	{Name: "Reddit", Category: CatSocial, Domains: []string{"reddit.com", "redd.it", "redditmedia.com", "redditstatic.com"}},
	{Name: "Social (other)", Category: CatSocial, Domains: []string{"tinder.com", "gotinder.com", "vk.com", "vkontakte.ru", "userapi.com", "bumble.com", "pinterest.com", "pinimg.com", "linkedin.com", "licdn.com", "threads.net", "bsky.app"}},
	{Name: "X / Twitter", Category: CatSocial, Domains: []string{"twitter.com", "x.com", "twimg.com", "t.co"}},
	{Name: "Windows / Xbox updates", Category: CatUpdates, Domains: []string{"windowsupdate.com", "update.microsoft.com", "delivery.mp.microsoft.com", "assets1.xboxlive.com", "xvcf1.xboxlive.com"}},
	{Name: "Apple / iCloud", Category: CatUpdates, Domains: []string{"icloud.com", "icloud-content.com", "apple-cloudkit.com", "mzstatic.com", "swcdn.apple.com", "apple.com", "aaplimg.com"}},
	{Name: "Cloud storage", Category: CatUpdates, Domains: []string{"dropbox.com", "dropboxusercontent.com", "onedrive.live.com", "sharepoint.com", "drive.google.com", "googleusercontent.com"}},
	{Name: "AI & dev", Category: CatDev, Domains: []string{"github.com", "githubusercontent.com", "githubassets.com", "anthropic.com", "claude.ai", "openai.com", "chatgpt.com", "pypi.org", "docker.io"}},
	{Name: "Ads & tracking", Category: CatAds, Domains: []string{"doubleclick.net", "googlesyndication.com", "googleadservices.com", "google-analytics.com", "googletagmanager.com",
		"adnxs.com", "criteo.com", "scorecardresearch.com", "app-measurement.com", "adsrvr.org", "taboola.com", "outbrain.com"}},
}

var appIndex = func() map[string]*App {
	m := map[string]*App{}
	for i := range Apps {
		for _, d := range Apps[i].Domains {
			m[d] = &Apps[i]
		}
	}
	return m
}()

// AppByName returns the rule entry for an app name.
func AppByName(n string) *App {
	for i := range Apps {
		if Apps[i].Name == n {
			return &Apps[i]
		}
	}
	return nil
}

// CategoryOf returns the category of an app name (generic names included).
func CategoryOf(app string) string {
	if a := AppByName(app); a != nil {
		return a.Category
	}
	switch app {
	case AppWeb:
		return CatWeb
	case AppDNS, AppVPN:
		return CatOther
	}
	return CatOther
}

// FromName maps a host name to an app; known-but-unlisted names are
// "Web browsing". Empty input returns "".
func FromName(name string) string {
	if name == "" {
		return ""
	}
	parts := strings.Split(name, ".")
	for i := 0; i < len(parts); i++ {
		if a, ok := appIndex[strings.Join(parts[i:], ".")]; ok {
			return a.Name
		}
	}
	return AppWeb
}

// FromAddress maps a server address/port to an app using operator address
// ranges and characteristic UDP ports (used for nameless game traffic).
func FromAddress(addr netip.Addr, udp bool, port uint16) string {
	for i := range Apps {
		a := &Apps[i]
		inRange := false
		for _, p := range a.Prefixes {
			if p.Contains(addr) {
				inRange = true
				break
			}
		}
		if inRange {
			return a.Name
		}
	}
	if udp {
		switch {
		case port == 3074 || port == 3544:
			return "Xbox Live"
		case port >= 27000 && port <= 27100:
			return "Steam"
		}
	}
	return ""
}

// BaseDomain returns the registrable part of a name (approximation).
func BaseDomain(name string) string {
	p := strings.Split(name, ".")
	n := len(p)
	if n <= 2 {
		return name
	}
	two := p[n-2] + "." + p[n-1]
	switch two {
	case "co.uk", "com.au", "co.jp", "com.br", "co.nz", "com.mx", "co.in":
		return p[n-3] + "." + two
	}
	return two
}
