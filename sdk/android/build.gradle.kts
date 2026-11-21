import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// Outflare for Android. It uses nothing from the Android SDK at compile
// time (the phone's details are read by reflection), so it builds and
// tests on any JVM and an Android app takes it as a plain library.
plugins {
    kotlin("jvm") version "2.4.21"
    `maven-publish`
    signing
}

group = "ai.outflare"
version = "0.110.0"

repositories {
    mavenCentral()
}

dependencies {
    testImplementation(kotlin("test"))
}

java {
    sourceCompatibility = JavaVersion.VERSION_11
    targetCompatibility = JavaVersion.VERSION_11
    withSourcesJar()
    withJavadocJar()
}

kotlin {
    compilerOptions {
        jvmTarget.set(JvmTarget.JVM_11)
    }
}

tasks.test {
    useJUnitPlatform()
}

// Maven Central takes a release as one signed bundle (docs/releasing.md,
// "Libraries"): `gradle publish` writes the library, its sources, its
// documentation jar and its description under build/central, with the
// checksums Central asks for, and Publish libraries zips and uploads that.
publishing {
    publications {
        create<MavenPublication>("maven") {
            artifactId = "outflare-android"
            from(components["java"])
            pom {
                name.set("Outflare for Android")
                description.set("Send screens and events from an Android app to Outflare.")
                url.set("https://outflare.ai/docs/sdk/mobile/")
                licenses {
                    license {
                        name.set("Apache-2.0")
                        url.set("https://www.apache.org/licenses/LICENSE-2.0.txt")
                    }
                }
                developers {
                    developer {
                        id.set("outflare")
                        name.set("Outflare")
                        email.set("hello@outflare.ai")
                        organization.set("Outflare")
                        organizationUrl.set("https://outflare.ai")
                    }
                }
                scm {
                    url.set("https://github.com/outflareai/outflare/tree/main/sdk/android")
                    connection.set("scm:git:https://github.com/outflareai/outflare.git")
                    developerConnection.set("scm:git:https://github.com/outflareai/outflare.git")
                }
            }
        }
    }
    repositories {
        maven {
            name = "central"
            url = uri(layout.buildDirectory.dir("central"))
        }
    }
}

// Signed only where a key is given (the release workflow): a build or test
// on anyone's machine needs none. The key is the armoured private key and
// its passphrase, from the environment, never from a file.
signing {
    val key = System.getenv("MAVEN_SIGNING_KEY")
    isRequired = !key.isNullOrBlank()
    if (!key.isNullOrBlank()) {
        useInMemoryPgpKeys(key, System.getenv("MAVEN_SIGNING_PASSWORD") ?: "")
        sign(publishing.publications["maven"])
    }
}
