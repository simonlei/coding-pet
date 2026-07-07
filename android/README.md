# CodingPet Viewer (Android)

一个用于展示 `coding-pet-server` 页面的 Android 应用。

## 特性
- **全屏 WebView**：加载并展示 agent-server 提供的 Web 页面（默认 `http://10.0.2.2:3000/`，模拟器映射到宿主机；真机请改成局域网 IP，例如 `http://192.168.x.x:3000/`）
- **防锁屏**：使用 `FLAG_KEEP_SCREEN_ON`，应用在前台时系统不会自动锁屏
- **伪黑屏时段**：在自定义时间段内，应用仍然活跃、WebView 仍在刷新，只是叠加黑色遮罩 + 系统亮度降到最低，看起来像"熄屏"
  - 默认：18:00 → 次日 09:00
  - 支持跨天区间
  - 可自定义"黑屏亮度"（0~100%，0 = 全黑）
  - 触摸屏幕即可临时唤醒 10 秒
- **可视化配置**：右下角设置按钮进入设置页，修改 URL、开关、起止时间、亮度

## 目录结构
```
coding-pet-android/
├── app/
│   ├── build.gradle.kts
│   └── src/main/
│       ├── AndroidManifest.xml
│       ├── java/com/codingpet/viewer/
│       │   ├── MainActivity.kt       # WebView + 遮罩控制
│       │   ├── SettingsActivity.kt   # 设置页
│       │   └── Prefs.kt              # 时段计算与偏好持久化
│       └── res/                      # 布局/主题/字符串
├── build.gradle.kts
├── settings.gradle.kts
└── gradle.properties
```

## 构建
```bash
# 需要 JDK 17 + Android SDK 34
# 首次执行会自动下载 gradle 8.7
./gradlew assembleDebug
# 产物: app/build/outputs/apk/debug/app-debug.apk
```

Windows PowerShell:
```powershell
.\gradlew.bat assembleDebug
```

## 使用
1. 启动 `coding-pet-server`（默认 3000 端口）：
   ```bash
   go run ./cmd/server -port 3000
   ```
2. 安装 APK 到手机/模拟器
3. 打开应用 → 右下角设置 → 修改"Agent Server 地址"为你机器的可访问 URL
4. 应用会保持屏幕常亮；在配置的时段内屏幕变黑但页面仍在实时刷新

## 关于"黑屏但保持活跃"的实现
- `WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON`：阻止系统锁屏
- 全屏黑色 `View` 遮罩：视觉上遮蔽内容
- `window.attributes.screenBrightness`：降低背光到极低值（无需系统权限，仅影响当前应用窗口）
- 定时器：每次到达"下一次切换时刻"或最多 60s 兜底触发一次状态刷新

## CI/CD

仓库根目录下已有 [`.github/workflows/android.yml`](../.github/workflows/android.yml)，会在以下情况触发：

- `push` 到 `main`/`master`（且改动涉及 `android/**`）
- 打 `v*` 标签
- Pull Request
- 手动 `workflow_dispatch`

产物：

- 每次构建都会上传 `android-apk-debug` 和 `android-apk-release` 两个 artifact
- 打 `v*` 标签时，APK 会自动附加到对应的 GitHub Release

### 配置 Release 签名（可选）

未配置密钥时，release APK 会以 `unsigned` 形式产出（可用于本地验证，无法安装）。要产出可安装的签名版，请在仓库 Settings → Secrets 中添加：

| Secret 名 | 说明 |
|-----------|------|
| `ANDROID_KEYSTORE_BASE64` | keystore 文件的 base64（`base64 -w0 release.keystore`） |
| `ANDROID_KEYSTORE_PASSWORD` | keystore 密码 |
| `ANDROID_KEY_ALIAS` | key 别名 |
| `ANDROID_KEY_PASSWORD` | key 密码 |

生成签名 keystore（本地一次）：
```bash
keytool -genkey -v -keystore release.keystore -alias codingpet \
  -keyalg RSA -keysize 2048 -validity 10000
base64 -w0 release.keystore > release.keystore.b64
```

配置好后再打一个 `v*` tag，即可获得已签名的 release APK。
