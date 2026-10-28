package api

import "strings"

// bannerTexts are the words on the consent banner. A workspace can change
// any of them per language (texts in the banner's settings); what it leaves
// empty comes from bannerDefaults, in the visitor's language when Outflare
// has it and in English otherwise.
type bannerTexts struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	Accept    string `json:"accept"`
	Reject    string `json:"reject"`
	Customize string `json:"customize"`
	Save      string `json:"save"`
	// The link to the cookie policy.
	Policy string `json:"policy"`
	// The button that opens the banner again.
	Settings string `json:"settings"`
	// Where US state laws let people opt out of the sale or sharing of
	// their data: the reject button's words there.
	OptOut string `json:"opt_out"`
	// Where the banner only informs (notice): the button that closes it.
	OK                 string `json:"ok"`
	Necessary          string `json:"necessary"`
	NecessaryDesc      string `json:"necessary_desc"`
	Analytics          string `json:"analytics"`
	AnalyticsDesc      string `json:"analytics_desc"`
	Marketing          string `json:"marketing"`
	MarketingDesc      string `json:"marketing_desc"`
	Preferences        string `json:"preferences"`
	PreferencesDesc    string `json:"preferences_desc"`
	AlwaysOn           string `json:"always_on"`
	GPCHonoured        string `json:"gpc_honoured"`
	OptOutConfirmation string `json:"opt_out_confirmation"`
}

// fields lists every text with its key, for merging and checking.
func (t *bannerTexts) fields() []*string {
	return []*string{&t.Title, &t.Body, &t.Accept, &t.Reject, &t.Customize, &t.Save, &t.Policy, &t.Settings, &t.OptOut, &t.OK,
		&t.Necessary, &t.NecessaryDesc, &t.Analytics, &t.AnalyticsDesc, &t.Marketing, &t.MarketingDesc,
		&t.Preferences, &t.PreferencesDesc, &t.AlwaysOn, &t.GPCHonoured, &t.OptOutConfirmation}
}

// over is t with its empty texts taken from d.
func (t bannerTexts) over(d bannerTexts) bannerTexts {
	mine, theirs := t.fields(), d.fields()
	for i := range mine {
		if strings.TrimSpace(*mine[i]) == "" {
			*mine[i] = *theirs[i]
		}
	}
	return t
}

// bannerDefaults are the banner's words in the languages Outflare has.
// Accept and reject are always the same length of plain words, so neither
// stands out (equal prominence, as European regulators require).
var bannerDefaults = map[string]bannerTexts{
	"en": {
		Title:              "We value your privacy",
		Body:               "We use cookies and similar technologies to run this site, measure how it is used and show relevant ads. Choose which ones you allow. You can change your mind at any time.",
		Accept:             "Accept all",
		Reject:             "Reject all",
		Customize:          "Customize",
		Save:               "Save choices",
		Policy:             "Cookie policy",
		Settings:           "Cookie settings",
		OptOut:             "Do not sell or share my personal information",
		OK:                 "OK",
		Necessary:          "Necessary",
		NecessaryDesc:      "Needed for the site to work, such as keeping you signed in and remembering this choice.",
		Analytics:          "Analytics",
		AnalyticsDesc:      "Help us understand how the site is used, so we can improve it.",
		Marketing:          "Marketing",
		MarketingDesc:      "Used to show and measure ads, here and on other sites.",
		Preferences:        "Preferences",
		PreferencesDesc:    "Remember settings such as your language or region.",
		AlwaysOn:           "Always on",
		GPCHonoured:        "Your browser's Global Privacy Control signal is honoured: we don't sell or share your data.",
		OptOutConfirmation: "You have opted out of the sale and sharing of your personal information.",
	},
	"de": {
		Title:              "Wir respektieren Ihre Privatsphäre",
		Body:               "Wir verwenden Cookies und ähnliche Technologien, um diese Website zu betreiben, ihre Nutzung zu messen und passende Werbung zu zeigen. Wählen Sie, was Sie erlauben. Sie können Ihre Wahl jederzeit ändern.",
		Accept:             "Alle akzeptieren",
		Reject:             "Alle ablehnen",
		Customize:          "Anpassen",
		Save:               "Auswahl speichern",
		Policy:             "Cookie-Richtlinie",
		Settings:           "Cookie-Einstellungen",
		OptOut:             "Meine personenbezogenen Daten nicht verkaufen oder teilen",
		OK:                 "OK",
		Necessary:          "Notwendig",
		NecessaryDesc:      "Für den Betrieb der Website nötig, etwa um Sie angemeldet zu halten und diese Wahl zu speichern.",
		Analytics:          "Statistik",
		AnalyticsDesc:      "Hilft uns zu verstehen, wie die Website genutzt wird, damit wir sie verbessern können.",
		Marketing:          "Marketing",
		MarketingDesc:      "Wird genutzt, um Werbung hier und auf anderen Websites zu zeigen und zu messen.",
		Preferences:        "Präferenzen",
		PreferencesDesc:    "Speichert Einstellungen wie Ihre Sprache oder Region.",
		AlwaysOn:           "Immer aktiv",
		GPCHonoured:        "Das Global-Privacy-Control-Signal Ihres Browsers wird beachtet: Wir verkaufen oder teilen Ihre Daten nicht.",
		OptOutConfirmation: "Sie haben dem Verkauf und der Weitergabe Ihrer personenbezogenen Daten widersprochen.",
	},
	"fr": {
		Title:              "Nous respectons votre vie privée",
		Body:               "Nous utilisons des cookies et des technologies similaires pour faire fonctionner ce site, mesurer son utilisation et afficher des publicités pertinentes. Choisissez ce que vous autorisez. Vous pouvez changer d'avis à tout moment.",
		Accept:             "Tout accepter",
		Reject:             "Tout refuser",
		Customize:          "Personnaliser",
		Save:               "Enregistrer mes choix",
		Policy:             "Politique de cookies",
		Settings:           "Paramètres des cookies",
		OptOut:             "Ne pas vendre ni partager mes informations personnelles",
		OK:                 "OK",
		Necessary:          "Nécessaires",
		NecessaryDesc:      "Indispensables au fonctionnement du site, par exemple pour rester connecté et mémoriser ce choix.",
		Analytics:          "Mesure d'audience",
		AnalyticsDesc:      "Nous aident à comprendre l'utilisation du site pour l'améliorer.",
		Marketing:          "Marketing",
		MarketingDesc:      "Servent à afficher et mesurer des publicités, ici et sur d'autres sites.",
		Preferences:        "Préférences",
		PreferencesDesc:    "Mémorisent des réglages comme votre langue ou votre région.",
		AlwaysOn:           "Toujours actifs",
		GPCHonoured:        "Le signal Global Privacy Control de votre navigateur est respecté : nous ne vendons ni ne partageons vos données.",
		OptOutConfirmation: "Vous vous êtes opposé à la vente et au partage de vos informations personnelles.",
	},
	"es": {
		Title:              "Respetamos tu privacidad",
		Body:               "Usamos cookies y tecnologías similares para que este sitio funcione, medir cómo se usa y mostrar anuncios relevantes. Elige cuáles permites. Puedes cambiar de opinión en cualquier momento.",
		Accept:             "Aceptar todo",
		Reject:             "Rechazar todo",
		Customize:          "Personalizar",
		Save:               "Guardar selección",
		Policy:             "Política de cookies",
		Settings:           "Configuración de cookies",
		OptOut:             "No vender ni compartir mi información personal",
		OK:                 "OK",
		Necessary:          "Necesarias",
		NecessaryDesc:      "Imprescindibles para que el sitio funcione, por ejemplo para mantener tu sesión y recordar esta elección.",
		Analytics:          "Analíticas",
		AnalyticsDesc:      "Nos ayudan a entender cómo se usa el sitio para mejorarlo.",
		Marketing:          "Marketing",
		MarketingDesc:      "Sirven para mostrar y medir anuncios, aquí y en otros sitios.",
		Preferences:        "Preferencias",
		PreferencesDesc:    "Recuerdan ajustes como tu idioma o región.",
		AlwaysOn:           "Siempre activas",
		GPCHonoured:        "Respetamos la señal Global Privacy Control de tu navegador: no vendemos ni compartimos tus datos.",
		OptOutConfirmation: "Te has opuesto a la venta y al intercambio de tu información personal.",
	},
	"it": {
		Title:              "Rispettiamo la tua privacy",
		Body:               "Usiamo cookie e tecnologie simili per far funzionare questo sito, misurarne l'uso e mostrare annunci pertinenti. Scegli quali consentire. Puoi cambiare idea in qualsiasi momento.",
		Accept:             "Accetta tutto",
		Reject:             "Rifiuta tutto",
		Customize:          "Personalizza",
		Save:               "Salva le scelte",
		Policy:             "Cookie policy",
		Settings:           "Impostazioni cookie",
		OptOut:             "Non vendere né condividere i miei dati personali",
		OK:                 "OK",
		Necessary:          "Necessari",
		NecessaryDesc:      "Servono al funzionamento del sito, ad esempio per mantenere l'accesso e ricordare questa scelta.",
		Analytics:          "Statistiche",
		AnalyticsDesc:      "Ci aiutano a capire come viene usato il sito, per migliorarlo.",
		Marketing:          "Marketing",
		MarketingDesc:      "Servono a mostrare e misurare annunci, qui e su altri siti.",
		Preferences:        "Preferenze",
		PreferencesDesc:    "Ricordano impostazioni come la lingua o la regione.",
		AlwaysOn:           "Sempre attivi",
		GPCHonoured:        "Rispettiamo il segnale Global Privacy Control del tuo browser: non vendiamo né condividiamo i tuoi dati.",
		OptOutConfirmation: "Ti sei opposto alla vendita e alla condivisione dei tuoi dati personali.",
	},
	"nl": {
		Title:              "We respecteren je privacy",
		Body:               "We gebruiken cookies en vergelijkbare technieken om deze site te laten werken, het gebruik te meten en relevante advertenties te tonen. Kies wat je toestaat. Je kunt je keuze altijd wijzigen.",
		Accept:             "Alles accepteren",
		Reject:             "Alles weigeren",
		Customize:          "Aanpassen",
		Save:               "Keuze opslaan",
		Policy:             "Cookiebeleid",
		Settings:           "Cookie-instellingen",
		OptOut:             "Mijn persoonsgegevens niet verkopen of delen",
		OK:                 "OK",
		Necessary:          "Noodzakelijk",
		NecessaryDesc:      "Nodig om de site te laten werken, bijvoorbeeld om je ingelogd te houden en deze keuze te onthouden.",
		Analytics:          "Statistieken",
		AnalyticsDesc:      "Helpen ons te begrijpen hoe de site wordt gebruikt, zodat we hem kunnen verbeteren.",
		Marketing:          "Marketing",
		MarketingDesc:      "Worden gebruikt om advertenties te tonen en te meten, hier en op andere sites.",
		Preferences:        "Voorkeuren",
		PreferencesDesc:    "Onthouden instellingen zoals je taal of regio.",
		AlwaysOn:           "Altijd aan",
		GPCHonoured:        "Het Global Privacy Control-signaal van je browser wordt gerespecteerd: we verkopen of delen je gegevens niet.",
		OptOutConfirmation: "Je hebt bezwaar gemaakt tegen het verkopen en delen van je persoonsgegevens.",
	},
	"pt": {
		Title:              "Respeitamos a sua privacidade",
		Body:               "Usamos cookies e tecnologias semelhantes para fazer este site funcionar, medir a sua utilização e mostrar anúncios relevantes. Escolha o que permite. Pode mudar de ideias a qualquer momento.",
		Accept:             "Aceitar tudo",
		Reject:             "Rejeitar tudo",
		Customize:          "Personalizar",
		Save:               "Guardar escolhas",
		Policy:             "Política de cookies",
		Settings:           "Definições de cookies",
		OptOut:             "Não vender nem partilhar as minhas informações pessoais",
		OK:                 "OK",
		Necessary:          "Necessários",
		NecessaryDesc:      "Indispensáveis para o site funcionar, por exemplo para manter a sessão e lembrar esta escolha.",
		Analytics:          "Estatísticas",
		AnalyticsDesc:      "Ajudam-nos a perceber como o site é usado, para o melhorar.",
		Marketing:          "Marketing",
		MarketingDesc:      "Usados para mostrar e medir anúncios, aqui e noutros sites.",
		Preferences:        "Preferências",
		PreferencesDesc:    "Lembram definições como o idioma ou a região.",
		AlwaysOn:           "Sempre ativos",
		GPCHonoured:        "O sinal Global Privacy Control do seu navegador é respeitado: não vendemos nem partilhamos os seus dados.",
		OptOutConfirmation: "Opôs-se à venda e partilha das suas informações pessoais.",
	},
}

// bannerLanguage is the language Outflare shows for a visitor's
// navigator.language ("de-AT" is de): one the workspace wrote texts for,
// else one Outflare has, else English.
func bannerLanguage(lang string, own map[string]bannerTexts) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	base, _, _ := strings.Cut(lang, "-")
	for _, l := range []string{lang, base} {
		if _, ok := own[l]; ok && l != "" {
			return l
		}
		if _, ok := bannerDefaults[l]; ok {
			return l
		}
	}
	return "en"
}

// bannerTextsFor is every text for one language: the workspace's own,
// then Outflare's in that language, then English.
func bannerTextsFor(lang string, own map[string]bannerTexts) bannerTexts {
	base, _, _ := strings.Cut(lang, "-")
	t := own[lang]
	if base != lang {
		t = t.over(own[base])
	}
	return t.over(bannerDefaults[base]).over(own["en"]).over(bannerDefaults["en"])
}
