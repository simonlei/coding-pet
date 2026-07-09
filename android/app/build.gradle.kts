import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// 从环境变量读取签名配置(供 CI 使用)
val ksPath: String? = System.getenv("ANDROID_KEYSTORE_PATH")
val ksPassword: String? = System.getenv("ANDROID_KEYSTORE_PASSWORD")
val keyAlias: String? = System.getenv("ANDROID_KEY_ALIAS")
val keyPassword: String? = System.getenv("ANDROID_KEY_PASSWORD")
val hasReleaseSigning = !ksPath.isNullOrBlank() && !ksPassword.isNullOrBlank()
        && !keyAlias.isNullOrBlank() && !keyPassword.isNullOrBlank()
        && file(ksPath!!).exists()

// 版本号解析:与 Go 端 ldflags 注入保持一致的思路
// CI: GITHUB_REF_NAME=android-vX.Y.Z(tag 触发) 或 分支名/dev-<sha>(非 tag)
// 本地: 无 GITHUB_REF_NAME,回退 "0.1.1"
// versionName 存 "X.Y.Z"(不带 v);非 semver(如 dev-abc)时置为 "0.0.0" 便于自更新识别为需升级
val rawRef: String = (System.getenv("GITHUB_REF_NAME") ?: "0.1.1").trim()
// 先剥掉 android- 前缀(如果存在),再交给 semver 正则
val refForSemver: String = rawRef.removePrefix("android-")
val semverRegex = Regex("^v?(\\d+)\\.(\\d+)\\.(\\d+)(?:[-+].*)?$")
val semverMatch = semverRegex.matchEntire(refForSemver)
val appVersionName: String = if (semverMatch != null) {
    val (maj, min, pat) = semverMatch.destructured
    "$maj.$min.$pat"
} else {
    // 非 semver(如 dev-abc123 / master / feature-x):写 0.0.0 便于更新逻辑视为最旧版本
    "0.0.0"
}
val appVersionCode: Int = if (semverMatch != null) {
    val (maj, min, pat) = semverMatch.destructured
    maj.toInt() * 10000 + min.toInt() * 100 + pat.toInt()
} else {
    1
}
// 便于自更新时保留原始 tag 字符串(rawRef 可能是 v0.1.1 或 dev-abc)
val appBuildRef: String = rawRef

android {
    namespace = "com.codingpet.viewer"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.codingpet.viewer"
        minSdk = 24
        targetSdk = 34
        versionCode = appVersionCode
        versionName = appVersionName
        // 供 UpdateChecker 读取原始构建引用(如 "v0.1.1" 或 "dev-abcdef")
        buildConfigField("String", "BUILD_REF", "\"${appBuildRef}\"")
    }

    if (hasReleaseSigning) {
        signingConfigs {
            create("release") {
                storeFile = file(ksPath!!)
                storePassword = ksPassword
                this.keyAlias = keyAlias
                this.keyPassword = keyPassword
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            if (hasReleaseSigning) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        viewBinding = true
        buildConfig = true
    }
}

dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.preference:preference-ktx:1.2.1")
    implementation("androidx.constraintlayout:constraintlayout:2.1.4")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")
}


