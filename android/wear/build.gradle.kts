plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// 签名：优先用环境变量（GitHub Secrets），否则用仓库里的 signing/shuku.jks。手机和手表必须用同一个签名
fun envOr(name: String, def: String) = System.getenv(name)?.takeIf { it.isNotBlank() } ?: def

android {
    namespace = "app.shuku.wear"
    compileSdk = 35

    defaultConfig {
        // 和手机 App 同一个包名，Wear OS 才会把两边当成同一个应用互相通信
        applicationId = "app.shuku"
        minSdk = 30
        targetSdk = 34
        versionCode = (project.property("shuku.versionCode") as String).toInt() + 100000
        versionName = project.property("shuku.versionName") as String
    }

    signingConfigs {
        create("shuku") {
            storeFile = file(envOr("SHUKU_KEYSTORE_FILE", rootProject.file("signing/shuku.jks").path))
            storePassword = envOr("SHUKU_KEYSTORE_PASSWORD", "shuku-app")
            keyAlias = envOr("SHUKU_KEY_ALIAS", "shuku")
            keyPassword = envOr("SHUKU_KEY_PASSWORD", "shuku-app")
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("shuku")
        }
        debug {
            signingConfig = signingConfigs.getByName("shuku")
        }
    }

    sourceSets["main"].java.srcDirs("src/main/java", "../common/src")

    buildFeatures { buildConfig = true }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }

    lint {
        abortOnError = false
        checkReleaseBuilds = false
    }

    packaging {
        resources {
            // 表盘复杂功能的库会带进 kotlinx-coroutines；有的版本 jar 里多了这个编译缓存文件，两份同名时打包会报重复
            excludes += "META-INF/versions/9/previous-compilation-data.bin"
        }
    }
}

dependencies {
    implementation("com.google.android.gms:play-services-wearable:18.2.0")
    // Tile 和表盘复杂功能。都是已经发布的稳定版：tiles 1.4.1 和 protolayout 1.2.1 是同一天（2024-10-16）发布的一对，
    // 表盘复杂功能 1.2.1 是官方文档里用的版本。只用了 protolayout 最基础的元素，没用 material
    implementation("androidx.wear.tiles:tiles:1.4.1")
    implementation("androidx.wear.protolayout:protolayout:1.2.1")
    implementation("androidx.wear.protolayout:protolayout-expression:1.2.1")
    implementation("androidx.wear.watchface:watchface-complications-data-source:1.2.1")
    // TileService 要返回 ListenableFuture（用 CallbackToFutureAdapter 做）；1.1.0 是 2020 年就发布的稳定版，tiles 本身也用这个库
    implementation("androidx.concurrent:concurrent-futures:1.1.0")
}
