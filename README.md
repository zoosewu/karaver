# Karaver

自架的多房間卡拉 OK 點歌系統。手機掃 QR code 點歌，電視或電腦上開播放頁。

- **點歌頁** `/r/<房間代碼>`：匿名加入（只需暱稱）、搜尋點歌、看佇列、刪自己的歌、切歌；輪到自己時可暫停、重唱、調音量、隱藏 QR code
- **播放頁** `/r/<房間代碼>/player`：不需登入，先到先播，後開的排隊遞補。全螢幕播放、換歌前 5 秒預告、角落顯示 QR code 與下一首、待機時顯示大 QR code
- **管理頁** `/admin`：建立／刪除房間、查看與中斷播放端、佇列模式（依點歌順序／輪流制）、調整順序、踢人、每人點歌上限、閒置自動清空、演唱紀錄、重新掃描曲庫

## 部署

不需要原始碼。建立一個資料夾，放入下面這份 `docker-compose.yml`，把標示「改成你的」的三行改掉：

```yaml
services:
  karaver:
    image: ghcr.io/zoosewu/karaver:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      # 改成你的：對外網址，QR code 會指向這裡（例如區網 IP 或反向代理後的網域）
      PUBLIC_URL: http://192.168.1.10:8080
      # 改成你的：管理頁密碼
      ADMIN_PASSWORD: change-me
      # 以下為選填，數值為預設值
      FILENAME_FORMAT: artist-title   # artist-title / title-artist / title
      FILENAME_SEPARATOR: " - "
      ORIGINAL_SUFFIX: ""
      MEDIA_EXTENSIONS: .mp4
      GOMEMLIMIT: 64MiB
    volumes:
      - karaver-data:/data
      # 改成你的：主機上的歌曲資料夾（唯讀掛載）
      - /path/to/karaoke:/media:ro

volumes:
  karaver-data:
```

```sh
docker compose up -d
```

如果比較想把設定放在 `.env`，也可以直接使用 repo 裡的 [`docker-compose.yml`](docker-compose.yml) 搭配 [`.env.example`](.env.example)：

```sh
curl -fsSLO https://raw.githubusercontent.com/zoosewu/karaver/main/docker-compose.yml
curl -fsSL https://raw.githubusercontent.com/zoosewu/karaver/main/.env.example -o .env
# 編輯 .env 後
docker compose up -d
```

接著開 `http://<主機>:8080/admin` 建立房間，再從房間的「開啟播放頁」把播放畫面投到電視上。

更新到最新版：

```sh
docker compose pull && docker compose up -d
```

Image 放在 `ghcr.io/zoosewu/karaver`，支援 `linux/amd64` 和 `linux/arm64`（例如樹莓派、ARM 架構的 NAS）。可用的標籤：

| 標籤 | 內容 |
|---|---|
| `latest` | `main` 分支的最新版 |
| `1.2.3`、`1.2`、`1` | 發行版（推送 `v1.2.3` 這種 git tag 時產生） |
| `sha-abc1234` | 特定 commit |

要固定版本時，把 `image` 改成 `ghcr.io/zoosewu/karaver:1.2.3`；使用 `.env` 的話，則設定 `KARAVER_TAG=1.2.3`。

如果想從原始碼自己建置：

```sh
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

### 環境變數

| 變數 | 說明 |
|---|---|
| `PUBLIC_URL` | 對外網址，QR code 會指向 `${PUBLIC_URL}/r/<房間代碼>`（必填） |
| `ADMIN_PASSWORD` | 管理頁的密碼（必填） |
| `MEDIA_PATH` | 僅限 `.env` 版本：主機上的歌曲資料夾（必填） |
| `PORT` | 僅限 `.env` 版本：對外 port，預設 8080 |
| `FILENAME_FORMAT` | `artist-title`（預設）、`title-artist`、`title` |
| `FILENAME_SEPARATOR` | 歌手和歌名之間的分隔字串，預設 `" - "`。找不到分隔字串時，整個檔名會當成歌名 |
| `ORIGINAL_SUFFIX` | 預留給原唱版影片使用，見下方說明 |
| `MEDIA_EXTENSIONS` | 要掃描的副檔名，預設 `.mp4` |

用反向代理（Caddy、Nginx、Traefik）提供 HTTPS 時，`PUBLIC_URL` 要填代理後的網址。代理需要支援 WebSocket（路徑 `/api/rooms/*/ws`）。

### 曲庫

- 容器啟動時會自動掃描一次，之後新增檔案請到管理頁按「重新掃描」。
- 資料夾結構不影響分類，只會解析檔名。
- 影片會以 HTTP Range 直接串流原始檔，不轉檔。請使用瀏覽器能播放的 H.264/AAC 格式；H.265 在部分瀏覽器上無法播放。
- 原唱版（預留功能）：設定 `ORIGINAL_SUFFIX=_original` 後，`周杰倫 - 晴天_original.mp4` 不會出現在歌單裡，而是記錄為 `周杰倫 - 晴天.mp4` 的原唱版。目前只有記錄，還沒有切換介面。

## 佇列規則

- **依點歌順序**：先點先唱，管理員可以手動調整順序。
- **輪流制**：每輪每人唱一首；同一輪中，最久沒唱的人先唱。從輪流制切回依點歌順序時，會保留當下的順序。
- 同一首歌已經在佇列中（或正在播放）時，不能重複點。
- 任何人都能切歌。暫停、重唱、音量和 QR code 顯示，只有點歌者本人和管理員能控制。
- 播放端先到先贏：第一個連上的負責播放，之後開的會排隊並顯示順位。播放中的播放端斷線時，排第一位的會自動接手，從目前這首歌的開頭播起。管理員可以在管理頁中斷任何一個播放端；被中斷的播放端按「重新連線」後，會排到隊伍最後面。
- 「播完換下一首」只接受目前播放中的播放端回報，並用伺服器發給它的密鑰驗證。

## 開發

```sh
# 後端（需要 Go 1.26）
cd server && PUBLIC_URL=http://localhost:5173 ADMIN_PASSWORD=dev MEDIA_DIR=/path/to/songs DATA_DIR=./data go run .
# 前端（Vite 會把 /api、/media 轉給 :8080）
cd web && npm install && npm run dev
# 測試
cd server && go test ./...
cd web && npm run check
```

架構：Go（`net/http`、`coder/websocket`、純 Go 的 SQLite）+ Svelte 5，前端打包後嵌入 Go 執行檔，最終產出單一 distroless image。房間狀態放在記憶體中，同時寫入 SQLite；有變動時透過 WebSocket 推送完整快照給所有連線。

介面文字放在 `web/src/lib/locales/`。要新增語言，複製 `zh-TW.ts` 後在 `web/src/lib/i18n.ts` 註冊即可。

## CI/CD

`.github/workflows/ci.yml` 的流程：

1. **測試**：每次 push 和 PR 都會執行 `gofmt`、`go vet`、`go test`、`svelte-check` 和前端建置。
2. **建置 image**：測試通過後建置 amd64 和 arm64 的 image。只有推送到 `main` 或推送 `v*` tag 時才會上傳到 GHCR，PR 只建置不上傳。

發行新版本：

```sh
git tag v1.0.0 && git push origin v1.0.0
```
