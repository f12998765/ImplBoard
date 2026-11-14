import java.util.Properties

plugins {
    id("com.android.application")
}

// 签名配置来自仓库内的 keystore.properties（内部自签名，见该文件注释）。
val keystoreProps = Properties().apply {
    val f = rootProject.file("keystore.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

android {
    namespace = "com.xizero.implboard"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.xizero.implboard"
        minSdk = 24
        targetSdk = 35

        // CI 用 -PappVersion=<version> 传入 mygo.json 里的版本号
        val version = (project.findProperty("appVersion") as String?) ?: "0.0.0"
        versionName = version
        versionCode = version.split(".").let { p ->
            (p.getOrNull(0)?.toIntOrNull() ?: 0) * 10000 +
                (p.getOrNull(1)?.toIntOrNull() ?: 0) * 100 +
                (p.getOrNull(2)?.toIntOrNull() ?: 0)
        }
    }

    signingConfigs {
        if (keystoreProps.getProperty("storeFile") != null) {
            create("release") {
                storeFile = rootProject.file(keystoreProps.getProperty("storeFile"))
                storePassword = keystoreProps.getProperty("storePassword")
                keyAlias = keystoreProps.getProperty("keyAlias")
                keyPassword = keystoreProps.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        getByName("release") {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release") ?: signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    packaging {
        jniLibs {
            useLegacyPackaging = false
        }
    }
}

dependencies {
    // gomobile bind 产出的 Go 服务（CI 里生成）
    implementation(files("libs/implboard.aar"))
}
