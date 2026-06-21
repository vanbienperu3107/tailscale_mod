# Hướng Dẫn Sử Dụng — Tailscale Portable (Windows)

> Bản **portable** chạy hoàn toàn không cài đặt: giải nén ra thư mục bất kỳ, chạy script, xong.  
> Tất cả trạng thái lưu trong thư mục đó — xóa thư mục là sạch hoàn toàn.

---

## Mục lục

1. [Tải về và giải nén](#1-tải-về-và-giải-nén)
2. [Cấu trúc thư mục](#2-cấu-trúc-thư-mục)
3. [Lần đầu — Đăng nhập](#3-lần-đầu--đăng-nhập)
4. [Chạy nền thủ công](#4-chạy-nền-thủ-công)
5. [Chạy nền tự động khi đăng nhập Windows](#5-chạy-nền-tự-động-khi-đăng-nhập-windows)
6. [LAN Proxy — Truy cập mạng nội bộ qua tailnet](#6-lan-proxy--truy-cập-mạng-nội-bộ-qua-tailnet)
7. [Cấu hình HTTP proxy (proxy.conf)](#7-cấu-hình-http-proxy-proxyconf)
8. [Kiểm tra trạng thái](#8-kiểm-tra-trạng-thái)
9. [Dừng Tailscale](#9-dừng-tailscale)
10. [Gỡ autostart](#10-gỡ-autostart)
11. [Di chuyển sang máy chủ mới](#11-di-chuyển-sang-máy-chủ-mới)
12. [Xử lý sự cố thường gặp](#12-xử-lý-sự-cố-thường-gặp)

---

## 1. Tải về và giải nén

Vào trang release của repo:

```
https://github.com/vanbienperu3107/tailscale_mod/releases/latest
```

Tải file `tailscale-mod-**portable**-windows-amd64-*.zip` (không phải bản `installer`).

Giải nén ra thư mục cố định, ví dụ:
```
C:\Tailscale\
```

> **Lưu ý:** Không để trong thư mục có dấu cách hoặc ký tự đặc biệt (tránh lỗi script).

---

## 2. Cấu trúc thư mục

Sau khi giải nén:

```
tailscale-mod-portable-windows-amd64-xxx\
 ├── tailscale.exe           CLI — gõ lệnh kiểm tra, ping, ...
 ├── tailscaled.exe          Daemon — tiến trình chính chạy nền
 ├── start-tailscale.bat     Khởi động (tự nhận diện vai trò itop/votam)
 ├── start-itop.bat          Khởi động ép vai trò itop (chia sẻ LAN)
 ├── start-votam.bat         Khởi động ép vai trò votam (dùng LAN)
 ├── stop-tailscale.bat      Dừng daemon
 ├── install-autostart.bat   Cài Scheduled Task → tự chạy nền khi login
 ├── uninstall-autostart.bat Gỡ Scheduled Task
 ├── run-hidden.vbs          Script ẩn (dùng bởi Scheduled Task)
 ├── proxy.conf              Cấu hình HTTP proxy đi ra ngoài
 ├── tailscale-proxy.pac     PAC file cho trình duyệt (LAN proxy)
 ├── metrics-report.ps1      Reporter latency/MAC tự động (tùy chọn)
 ├── test-lan.bat            Kiểm tra LAN proxy
 ├── gost.exe                Công cụ LAN proxy dự phòng
 ├── logs\                   Log tự động ghi vào đây
 └── state\                  Trạng thái đăng nhập (tạo tự động)
```

---

## 3. Lần đầu — Đăng nhập

**Bước 1:** Double-click `start-tailscale.bat`.

Script sẽ:
- Yêu cầu quyền Administrator (UAC)
- Khởi động `tailscaled.exe` chạy ẩn trong nền
- Tự nhận diện vai trò máy (itop/votam) dựa theo IP
- Chạy `tailscale up --unattended --login-server=https://vpn2.hangocthanh.io.vn`
- In ra link đăng nhập Google (OIDC)

**Bước 2:** Click vào link đăng nhập hiện ra trong CMD, đăng nhập Google trong trình duyệt.

**Bước 3:** Sau khi thấy "Connected. Status:" — xong. Cửa sổ CMD có thể đóng, **daemon vẫn chạy ẩn**.

> Trạng thái đăng nhập lưu vào thư mục `state\`. Các lần khởi động sau **không cần đăng nhập lại**.

---

## 4. Chạy nền thủ công

Mỗi lần muốn bật Tailscale:

```
Double-click start-tailscale.bat
```

`tailscaled.exe` chạy hoàn toàn ẩn (không có cửa sổ) nhờ:
```bat
powershell -NoProfile -Command "Start-Process -WindowStyle Hidden ... tailscaled.exe ..."
```

Cửa sổ CMD chỉ hiện trong lúc chờ kết nối — sau khi thấy "Connected" có thể **đóng cửa sổ CMD mà không làm chết daemon**.

Daemon chỉ dừng khi bạn chạy `stop-tailscale.bat` hoặc tắt máy.

---

## 5. Chạy nền tự động khi đăng nhập Windows

Cách này giúp Tailscale **tự bật mỗi khi bạn đăng nhập Windows — hoàn toàn ẩn, không hỏi UAC, không cửa sổ nào hiện ra**.

### Yêu cầu

- Đã hoàn thành [Bước 3 — Lần đầu đăng nhập](#3-lần-đầu--đăng-nhập) ít nhất 1 lần (để lưu state)
- Tài khoản Windows là Administrator

### Cài đặt

Double-click **`install-autostart.bat`** → nhấn Yes khi UAC hỏi.

Script tạo một **Windows Scheduled Task** tên `TailscalePortable`:

| Thuộc tính | Giá trị |
|-----------|---------|
| Trigger | Mỗi lần đăng nhập Windows (`ONLOGON`) |
| Chạy qua | `wscript.exe run-hidden.vbs` → hoàn toàn ẩn |
| Quyền | Highest (không hỏi UAC) |
| Tham số | `auto` → tự nhận diện itop/votam |

Sau khi cài, task chạy thử ngay lập tức để kiểm tra. Từ đó:

```
Bật máy → Đăng nhập Windows → Tailscale tự kết nối trong nền
```

### Cơ chế hoạt động chi tiết

```
Scheduled Task (ONLOGON, HIGHEST)
  └── wscript.exe run-hidden.vbs      (không cửa sổ)
        └── start-tailscale.bat auto  (chế độ tự nhận diện)
              ├── tailscaled.exe      (daemon ẩn)
              └── tailscale up        (kết nối lại, không cần browser)
```

### Gỡ cài đặt autostart

```
Double-click uninstall-autostart.bat
```

Lệnh thực thi: `schtasks /Delete /TN "TailscalePortable" /F`

Daemon đang chạy vẫn không bị ảnh hưởng — chỉ gỡ cơ chế tự khởi động.

---

## 6. LAN Proxy — Truy cập mạng nội bộ qua tailnet

Cho phép máy **votam** (ngoài mạng) truy cập mạng nội bộ `10.x.x.x` qua máy **itop** (trong mạng corp) thông qua tailnet.

### Vai trò

| Vai trò | Mô tả | Script |
|---------|-------|--------|
| **itop** | Máy trong mạng corp (IP `10.121.x.x`) — chia sẻ LAN | `start-itop.bat` |
| **votam** | Máy ngoài — muốn dùng LAN qua itop | `start-votam.bat` |

`start-tailscale.bat` **tự nhận diện** dựa theo IP: nếu máy có IP bắt đầu bằng `10.121.` → itop, ngược lại → votam.

### Cách 1: Native subnet routing (mặc định, không cần gost)

**Máy itop:**
- Script tự chạy: `tailscale up --advertise-routes=10.0.0.0/8`
- Server tự duyệt route (auto-approve đã bật sẵn)

**Máy votam:**
- Script tự chạy: `tailscale up --accept-routes`
- Bật proxy cho trình duyệt (1 trong 2 cách):

  **Cách a — PAC file (khuyên dùng):** Chỉ traffic `10.x.x.x` đi qua tailnet, còn lại DIRECT:
  ```
  Windows Settings → Network & Internet → Proxy → Use setup script:
  file:///C:\<đường-dẫn-thư-mục>\tailscale-proxy.pac
  ```

  **Cách b — SOCKS5 trực tiếp** cho trình duyệt:
  ```
  Host: 127.0.0.1   Port: 7654
  ```

**Sơ đồ:**
```
votam browser
  --(PAC: chỉ 10.x)--> SOCKS5 127.0.0.1:7654 (tailscale userspace)
  --(subnet route đã accept)--> itop --> 10.x.x.x
```

### Cách 2: Dự phòng qua gost (nếu native chưa thông)

**Máy itop:** Đổi `LAN_PROXY_MODE=itop-gost` trong `start-tailscale.bat`
- Chạy `gost.exe` cổng 18080 + `tailscale serve --tcp 18080`

**Máy votam:** Đổi `LAN_PROXY_MODE=votam-gost`
- Gost bridge: `127.0.0.1:18888 → SOCKS5:7654 → itop:18080`
- Trong PAC file đổi proxy thành `PROXY 127.0.0.1:18888`

**Kiểm tra nhanh:** Chạy `test-lan.bat` trên cả hai máy.

---

## 7. Cấu hình HTTP proxy (proxy.conf)

Dùng khi máy bạn **phải đi qua HTTP proxy của công ty** để ra Internet.

File `proxy.conf` nằm cùng thư mục với `tailscaled.exe`:

```json
{
  "enabled": true,
  "httpProxy":  "http://proxy.congty.com:8080",
  "httpsProxy": "http://proxy.congty.com:8080",
  "noProxy":    "127.0.0.1,localhost,*.local",
  "proxyAuth": { "username": "user", "password": "matkhau" }
}
```

| `enabled` | Hành vi |
|-----------|---------|
| `false` (mặc định) | Tắt hoàn toàn, kết nối trực tiếp |
| `true` | Bật, ghi đè HTTP_PROXY/HTTPS_PROXY |
| Xóa file | Dùng biến môi trường HTTP_PROXY/HTTPS_PROXY |

`proxyAuth` là tùy chọn — chỉ cần khi proxy yêu cầu xác thực Basic.

**Sau khi sửa:** Chạy `stop-tailscale.bat` rồi `start-tailscale.bat` để áp dụng.

**Kiểm tra đã áp dụng:** Mở file log mới nhất trong thư mục `logs\`, tìm dòng:
```
tshttpproxy: using proxy config from proxy.conf
```

### Môi trường sau HTTP proxy (UDP bị chặn)

`start-tailscale.bat` đã đặt sẵn 2 biến môi trường giúp hoạt động khi UDP bị chặn:

```bat
TS_DEBUG_ALWAYS_USE_DERP=1     → ép toàn bộ traffic qua DERP relay (TCP)
TS_DERP_KEEPALIVE_SECS=25      → ping DERP mỗi 25s để giữ tunnel khỏi bị đóng
```

Nếu mạng bình thường (UDP thông), có thể bỏ 2 dòng này để kết nối peer-to-peer nhanh hơn.

---

## 8. Kiểm tra trạng thái

Mở CMD bất kỳ trong thư mục Tailscale:

```bat
REM Xem trạng thái kết nối và danh sách peer
tailscale.exe status

REM Ping một peer trong tailnet
tailscale.exe ping <tên-máy-hoặc-IP-tailnet>

REM Xem thông tin mạng và DERP latency
tailscale.exe netcheck

REM Kiểm tra LAN proxy (chạy test-lan.bat)
test-lan.bat
```

Xem log realtime:
```
logs\tailscale-service-<ngày>-*.txt
```

---

## 9. Dừng Tailscale

```
Double-click stop-tailscale.bat
```

Lệnh thực thi:
```bat
tailscale.exe down          (ngắt kết nối VPN)
taskkill /IM tailscaled.exe /F   (dừng daemon)
```

> Nếu đã cài autostart, lần đăng nhập sau Tailscale vẫn tự bật lại. Để tắt vĩnh viễn, cần gỡ autostart trước.

---

## 10. Gỡ autostart

```
Double-click uninstall-autostart.bat
```

Xóa Scheduled Task `TailscalePortable`. Tailscale sẽ không tự bật nữa sau khi đăng nhập Windows.

---

## 11. Di chuyển sang máy chủ mới

> Theo quy tắc dự án: không dùng bản build trên hệ thống thực cho đến khi CI pass.

Khi server headscale chuyển sang IP/domain mới:

1. Sửa `HS_SERVER` trong `start-tailscale.bat`:
   ```bat
   set "HS_SERVER=https://vpn-moi.example.com"
   ```

2. Xóa thư mục `state\` (buộc đăng nhập lại với server mới):
   ```bat
   rmdir /s /q state
   mkdir state
   ```

3. Chạy `start-tailscale.bat` → đăng nhập lại Google.

4. Nếu có autostart, thư mục vẫn giữ nguyên — lần đăng nhập Windows tiếp theo tự dùng server mới.

---

## 12. Xử lý sự cố thường gặp

### Không kết nối được, màn hình "Waiting for the daemon..."

Xem log trong `logs\` — tìm dòng `control:` hoặc `derp:` để biết lỗi cụ thể.

Kiểm tra proxy: nếu đang sau HTTP proxy công ty, bật `proxy.conf` với địa chỉ proxy đúng.

### Daemon chạy nhưng không kết nối được tailnet

```bat
tailscale.exe status        → xem lý do (Needs login / Backend closed / ...)
tailscale.exe netcheck      → xem DERP latency và UDP có thông không
```

### Sau khi bật autostart, mỗi lần login vẫn hiện cửa sổ UAC

Task phải tạo với `/RL HIGHEST` bằng tài khoản **Administrator** (không phải user thường có quyền admin). Chạy lại `install-autostart.bat` bằng tài khoản đúng.

### Muốn ép vai trò thủ công thay vì tự nhận diện

```bat
start-tailscale.bat itop    → ép itop
start-tailscale.bat votam   → ép votam
```

Hoặc sửa trực tiếp trong `start-tailscale.bat`:
```bat
set "LAN_PROXY_MODE=itop"    REM hoặc votam, itop-gost, votam-gost
```

### Xóa hoàn toàn không để lại dấu vết

1. Chạy `stop-tailscale.bat`
2. Chạy `uninstall-autostart.bat` (nếu đã cài)
3. Xóa toàn bộ thư mục Tailscale

Không có registry, không có service Windows, không có file nào ngoài thư mục này.

---

*Tài liệu này mô tả bản `tailscale-mod-portable` — fork tự host với headscale tại `vpn2.hangocthanh.io.vn`. Không áp dụng cho bản Tailscale chính thức.*
