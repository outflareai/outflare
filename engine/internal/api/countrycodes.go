package api

import "strings"

// countryCodes lines up ISO 3166-1 alpha-3 codes, as Search Console names
// countries (lowercase), with alpha-2 codes, as rank trackers do
// (uppercase): five letters a country.
const countryCodes = "" +
	"abwAWafgAFagoAOaiaAIalaAXalbALandADareAEargARarmAMasmASataAQatfTFatgAGausAUautATazeAZbdiBIbelBEbenBJ" +
	"besBQbfaBFbgdBDbgrBGbhrBHbhsBSbihBAblmBLblrBYblzBZbmuBMbolBObraBRbrbBBbrnBNbtnBTbvtBVbwaBWcafCFcanCA" +
	"cckCCcheCHchlCLchnCNcivCIcmrCMcodCDcogCGcokCKcolCOcomKMcpvCVcriCRcubCUcuwCWcxrCXcymKYcypCYczeCZdeuDE" +
	"djiDJdmaDMdnkDKdomDOdzaDZecuECegyEGeriEReshEHespESestEEethETfinFIfjiFJflkFKfraFRfroFOfsmFMgabGAgbrGB" +
	"geoGEggyGGghaGHgibGIginGNglpGPgmbGMgnbGWgnqGQgrcGRgrdGDgrlGLgtmGTgufGFgumGUguyGYhkgHKhmdHMhndHNhrvHR" +
	"htiHThunHUidnIDimnIMindINiotIOirlIEirnIRirqIQislISisrILitaITjamJMjeyJEjorJOjpnJPkazKZkenKEkgzKGkhmKH" +
	"kirKIknaKNkorKRkwtKWlaoLAlbnLBlbrLRlbyLYlcaLClieLIlkaLKlsoLSltuLTluxLUlvaLVmacMOmafMFmarMAmcoMCmdaMD" +
	"mdgMGmdvMVmexMXmhlMHmkdMKmliMLmltMTmmrMMmneMEmngMNmnpMPmozMZmrtMRmsrMSmtqMQmusMUmwiMWmysMYmytYTnamNA" +
	"nclNCnerNEnfkNFngaNGnicNIniuNUnldNLnorNOnplNPnruNRnzlNZomnOMpakPKpanPApcnPNperPEphlPHplwPWpngPGpolPL" +
	"priPRprkKPprtPTpryPYpsePSpyfPFqatQAreuRErouROrusRUrwaRWsauSAsdnSDsenSNsgpSGsgsGSshnSHsjmSJslbSBsleSL" +
	"slvSVsmrSMsomSOspmPMsrbRSssdSSstpSTsurSRsvkSKsvnSIsweSEswzSZsxmSXsycSCsyrSYtcaTCtcdTDtgoTGthaTHtjkTJ" +
	"tklTKtkmTMtlsTLtonTOttoTTtunTNturTRtuvTVtwnTWtzaTZugaUGukrUAumiUMuryUYusaUSuzbUZvatVAvctVCvenVEvgbVG" +
	"virVIvnmVNvutVUwlfWFwsmWSyemYEzafZAzmbZMzweZW"

var alpha3To2, alpha2To3 = func() (map[string]string, map[string]string) {
	a, b := map[string]string{}, map[string]string{}
	for i := 0; i+5 <= len(countryCodes); i += 5 {
		three, two := countryCodes[i:i+3], countryCodes[i+3:i+5]
		a[three], b[two] = two, three
	}
	return a, b
}()

// countryAlpha3 is the lowercase alpha-3 code for an alpha-2 or alpha-3
// code, or "" when it is not a country.
func countryAlpha3(code string) string {
	switch len(code) {
	case 2:
		return alpha2To3[strings.ToUpper(code)]
	case 3:
		c := strings.ToLower(code)
		if _, ok := alpha3To2[c]; ok {
			return c
		}
	}
	return ""
}
