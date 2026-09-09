# CLIProxyAPI Usage Keeper

椤圭洰浠撳簱锛?https://github.com/Wu-M1ng/cpa-keeper-dashboard>

杞婚噺鍖栫殑 CLIProxyAPI 鍘熺敓鐢ㄩ噺鎻掍欢銆傚畠鎺ユ敹 CLIProxyAPI 宸插畬鎴愮殑 `UsageRecord`锛岀敤鏈夌晫鍐呭瓨闃熷垪寮傛鎵瑰啓 SQLite锛屽苟鎻愪緵涓変釜鍐呭祵椤甸潰锛氭€昏銆佹帴鍙ｃ€佽缃€?
## 璁捐杈圭晫

- 鍙０鏄?`usage_plugin` 鍜?`management_api`銆?- 涓嶅惎鍔ㄧ嫭绔?HTTP 鏈嶅姟锛屼笉杞 CPA锛屼笉浣跨敤 Redis銆?- `usage.handle` 鍙仛 JSON 瑙ｇ爜銆佽劚鏁忓拰闈為樆濉炲叆闃燂紝涓嶆墽琛岀鐩?I/O銆?- SQLite 浣跨敤 WAL銆佺煭浜嬪姟鍜?rollup 琛紱鎬昏/鍒嗘瀽鏌ヨ涓嶆壂鎻忎簨浠舵鏂囥€?- API Key 浠呬繚瀛樼缉鐣ユ樉绀哄€煎拰 HMAC 鍒嗙粍鍊硷紝澶辫触鏂囨湰浼氭埅鏂苟娓呯悊甯歌瀵嗛挜鏍煎紡銆?- 涓嶄娇鐢ㄥ搷搴旀嫤鎴垨娴佸紡鎷︽埅锛屽洜姝や笉浼氬鐞嗘瘡涓搷搴?Chunk銆?
## 鏋勫缓鍓嶆彁

CLIProxyAPI 鍘熺敓鎻掍欢闇€瑕?CGO銆俉indows 浣跨敤 MinGW-w64锛孡inux 浣跨敤 `gcc`/`musl-gcc`锛宮acOS 浣跨敤 Xcode Command Line Tools銆?
```powershell
cd go
$env:CGO_ENABLED = "1"
$env:GOSUMDB = "off" # 浠呭湪鏈満浠ｇ悊鏃犳硶璁块棶 sum.golang.org 鏃朵娇鐢?go mod download
go test ./...
go vet ./...
go build -buildmode=c-shared -o usage-keeper.dll .
```

Linux/macOS 灏嗚緭鍑烘枃浠跺悕鏀逛负 `usage-keeper.so` 鎴?`usage-keeper.dylib`銆?
涔熷彲浠ヤ娇鐢細

```powershell
.\scripts\build.ps1 -Version 1.6.0
```

## 瀹夎

鎶婂姩鎬佸簱鏀惧叆 CPA 鎻掍欢鐩綍锛屼緥濡?Windows锛?
```text
<cpa-workdir>/plugins/windows/amd64/usage-keeper.dll
```

鍦?`config.yaml` 涓紑鍚細

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    usage-keeper:
      enabled: true
      priority: 10
      storage_enabled: true
      storage_path: "data/usage-keeper.db"
      queue_size: 256
      batch_size: 64
      flush_interval_ms: 250
      retention_days: 30
      export_max_records: 50000
```

鍚姩鍚庨獙璇侊細

```text
GET /v0/management/plugins
GET /v0/resource/plugins/usage-keeper/dashboard
```

绠＄悊涓績闇€瑕佹樉绀?`registered: true` 涓?`effective_enabled: true`锛屽苟鍦ㄨ鎻掍欢鐨?`menus` 鏁扮粍涓湅鍒帮細

```json
{
  "path": "/v0/resource/plugins/usage-keeper/dashboard",
  "menu": "鐢ㄩ噺 Keeper"
}
```

璧勬簮椤甸潰浼氬嚭鐜板湪 CPA 绠＄悊涓績鐨勬彃浠惰彍鍗曚腑銆傛洿鏂?DLL/SO 鍚庨渶瑕侀噸鍚?CPA锛屾垨鎵ц涓€娆℃彃浠堕厤缃噸杞斤紝璁╁涓婚噸鏂拌皟鐢?`management.register`銆?
## 涓変釜椤甸潰

| 椤甸潰 | 鍐呭 |
| --- | --- |
| 鎬昏 | KPI銆佸仴搴风洃娴嬨€佽姹?Token/璐圭敤瓒嬪娍銆侀槦鍒楃姸鎬併€佸洓涓垎甯冨浘銆乀oken 鏋勬垚銆佹ā鍨嬬粺璁°€佽姹傛槑缁嗐€佺瓫閫夈€佸垎椤典笌 CSV 瀵煎嚭 |
| 鎺ュ彛 | API Key 缁熻銆佷笂娓哥粺璁°€丳rovider/Auth 涓婃父璇︽儏 |
| 璁剧疆 | 妯″瀷浠锋牸銆丼QLite/WAL/闃熷垪鐘舵€併€丣SON 澶囦唤涓庢仮澶?|

## Dashboard 鍛堢幇濂戠害

- 椤堕儴瀵艰埅鍥哄畾涓衡€滄€昏 / 鎺ュ彛 / 璁剧疆鈥濓紱鍒嗘瀽涓庝簨浠剁户缁悎骞跺湪鎬昏椤碉紝鎵€鏈夌幇鏈?ID銆佺瓫閫夈€佸垎椤点€佸鍑哄拰璇︽儏鎶藉眽淇濇寔鍙敤銆?- 鎬昏鎸夆€滄寚鏍囧甫 -> 杩愯鑴夋悘 -> 鍒嗘瀽鏋勬垚 -> 璇锋眰鏄庣粏鈥濆垎灞傚憟鐜般€傝秼鍔垮浘浠呯粯鍒跺凡鍔犺浇鐨勬湁鐢ㄩ噺鐐癸紝妯潗鏍囨寜鐪熷疄鏃堕棿鎴冲畾浣嶏紝鎮仠浣跨敤鍙傝€冪嚎鍜屽崟甯ф洿鏂般€?- 鍒嗗竷鍥剧殑鐧惧垎姣斾互褰撳墠鑼冨洿鍏ㄩ噺璇锋眰涓哄垎姣嶏紝Top 5 涔嬪鑱氬悎涓衡€滃叾浠栤€濓紝閬垮厤鎶?Top 5 璇姤涓哄叏閲忓崰姣旓紱Token 鍜岃〃鏍兼暟鍊肩粺涓€浣跨敤 K/M锛屽畬鏁存暟鍊间繚鐣欏湪 title/Tooltip 涓€?- 椤甸潰鍙鍙?CPA 瀹夸富鐨?`data-theme` / `data-cpa-theme`锛屼笉鍐欏叆鎻掍欢涓婚鍋忓ソ銆傞〉闈㈤殣钘忔椂鍋滄鍒锋柊锛岄〉闈㈠彲瑙佷笖鍓嶇缂撳瓨杩囨湡鍚庢墠鎸?`summary -> analysis -> events` 鍒嗛樁娈佃姹傘€?- CSS 涓哄祵鍏ュ紡鍗曟枃浠讹紝涓嶅姞杞藉閮ㄥ瓧浣撱€佸浘琛ㄥ簱鎴栧浘鐗囷紱绉诲姩绔姹傛槑缁嗕娇鐢?`data-label` 缃戞牸甯冨眬锛岄伩鍏嶉〉闈㈢骇妯悜婊氬姩銆?
## 浣庤礋杞界瓥鐣?
`usage.handle` 鐨勮矾寰勫涓嬶細

```text
UsageRecord -> compact event -> bounded channel -> return
                                      |
                                      v
                              one SQLite writer
```

闃熷垪榛樿瀹圭撼 256 鏉′簨浠躲€傞槦鍒楁弧鏃跺湪鏋勯€犲畬鏁翠簨浠跺墠璁板綍 `dropped` 璁℃暟骞剁珛鍗宠繑鍥烇紱鏈埛鏂版壒娆″湪杩涚▼绐佺劧閫€鍑烘椂鍙兘涓㈠け锛岃繖鏄伩鍏嶉樆濉?API 瀹屾垚璺緞鐨勬槑纭彇鑸嶃€傚悗鍙伴粯璁ゆ瘡 64 鏉℃垨 250 ms 鍐欎竴娆°€?
Dashboard 浠呭湪椤甸潰鍙涓?60 绉掑墠绔紦瀛樺け鏁堟椂鍒锋柊銆傝仛鍚?Management API 浣跨敤 4 绉掋€?2 鏉＄洰銆? MiB 涓婇檺鐨勮繘绋嬪唴缂撳瓨锛屽苟鍚堝苟鐩稿悓 Key 鐨勫苟鍙戞煡璇€係QLite 鏈€澶?4 涓繛鎺ワ紝姣忎釜杩炴帴绾?8 MiB 椤电紦瀛橈紝涓存椂鎺掑簭鍐欏叆涓存椂鏂囦欢銆?
## 瓒嬪娍涓庢暟鎹繚鐣?
- 瓒嬪娍鎸夊寳浜椂闂村垎缁勶細鏃ユ眹鎬讳粠闆剁偣寮€濮嬶紝鍛ㄦ眹鎬讳粠鍛ㄤ竴闆剁偣寮€濮嬨€傘€屽叏閮ㄣ€嶆牴鎹渶鏃╀繚鐣欒褰曞埌褰撳墠鏃堕棿鐨勮法搴﹂€夋嫨绮掑害锛?5 澶╀互鍐呮寜澶╂眹鎬伙紝鏇撮暱鍘嗗彶鎸夊懆姹囨€伙紱棣栦釜涓嶅畬鏁村懆鐨勬棩鏈熶粠鏈€鏃╀繚鐣欒褰曟墍鍦ㄦ棩鏈熷紑濮嬨€?- `retention_days` 鏄粴鍔ㄤ繚鐣欏ぉ鏁般€傚惎鍔ㄥ拰淇濆瓨瀛樺偍璁剧疆鏃舵墽琛屾竻鐞嗭紝鍚庡彴姣?24 灏忔椂鍐嶆墽琛屼竴娆★紝鍥犳涓ゆ娓呯悊涔嬮棿鍙兘鏆傜暀涓嶈冻涓€澶╃殑杩囨湡璁板綍銆?- 淇濆瓨瀛樺偍璁剧疆涓庢竻鐞嗗湪鍚屼竴涓簨鍔′腑瀹屾垚锛屽け璐ユ椂鍥炴粴骞惰繑鍥為敊璇€備簨浠跺拰鍒嗛挓姹囨€诲悓姝ユ竻鐞嗭紝鎴鏃堕棿鎵€鍦ㄥ垎閽熺殑姹囨€绘牴鎹繚鐣欒褰曢噸寤猴紱娓呯悊閿欒淇濈暀鍒颁笅涓€娆℃竻鐞嗘垚鍔熴€?- 椤甸潰淇濆瓨鐨勪繚鐣欏ぉ鏁板瓨鍏?SQLite锛岄噸鍚椂浼樺厛浜?YAML 閰嶇疆锛涢渶瑕佽皟鏁村凡淇濆瓨鐨勫€兼椂锛屽湪鎻掍欢銆岃缃€嶉〉闈慨鏀广€?
## 鍙戝竷

```powershell
.\scripts\build.ps1 -Version 1.6.0 -GoOS windows -GoArch amd64
```

鎺ㄩ€佽涔夊寲鐗堟湰鏍囩鍚庯紝GitHub Actions 浼氳嚜鍔ㄦ瀯寤?Windows/Linux/macOS 鍔ㄦ€佸簱锛屾墦鍖?zip锛岀敓鎴愮粺涓€鐨?`checksums.txt`锛屽苟鍒涘缓 GitHub Release锛?
```powershell
git tag v1.6.0
git push origin v1.6.0
```

涔熷彲鍦?GitHub Actions 鎵嬪姩杩愯 `release` 宸ヤ綔娴佸苟杈撳叆鐗堟湰鏍囩锛屼緥濡?`v1.6.0`銆傚伐浣滄祦浼氭鏌ヨ繙绔?tag锛氫笉瀛樺湪鏃跺湪褰撳墠鎻愪氦鍒涘缓骞舵帹閫侊紝瀛樺湪鏃剁洿鎺ュ鐢紱瀵瑰簲 GitHub Release 宸插瓨鍦ㄦ椂浼氭洿鏂板悓鍚嶅彂甯冭祫浜с€?
鍙戝竷 zip 鏍圭洰褰曠洿鎺ュ寘鍚姩鎬佸簱銆俙registry.json` 鏄彃浠跺晢搴楁潯鐩紝浠撳簱鍦板潃宸叉寚鍚戞湰椤圭洰銆?
