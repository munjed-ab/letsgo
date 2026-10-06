plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.letsgo.app"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.letsgo.app"
        minSdk = 24
        targetSdk = 34
        versionCode = 3
        versionName = "0.2.1"
        ndk { abiFilters += listOf("arm64-v8a") } // the .aar is arm64 only
    }
    buildTypes {
        release {
            // Shrunk (the icon library alone is ~30 MB unshrunk) and signed with the debug key, so it
            // installs straight away and upgrades in place. Publish under your own key if you distribute it.
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            signingConfig = signingConfigs.getByName("debug")
        }
    }
    packaging { jniLibs { useLegacyPackaging = true } } // compress the 12 MB Go library inside the APK
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures { compose = true }
    composeOptions { kotlinCompilerExtensionVersion = "1.5.14" } // matches Kotlin 1.9.24
}

dependencies {
    implementation(files("libs/letsgo.aar"))
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation(platform("androidx.compose:compose-bom:2024.06.00"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.activity:activity-compose:1.9.0")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.2")
}
