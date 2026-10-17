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
}

dependencies {
    implementation("com.google.android.gms:play-services-wearable:18.2.0")
}
