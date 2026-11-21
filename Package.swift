// swift-tools-version:5.9
// Outflare for iOS, macOS, tvOS and watchOS (sdk/swift). Swift Package
// Manager reads the manifest at the top of a repository, so it lives here:
// add https://github.com/outflareai/outflare to your app and pick a version.
// Nothing else in this repository is part of the package.
import PackageDescription

let package = Package(
    name: "Outflare",
    platforms: [.iOS(.v13), .macOS(.v10_15), .tvOS(.v13), .watchOS(.v6)],
    products: [
        .library(name: "Outflare", targets: ["Outflare"]),
    ],
    targets: [
        .target(name: "Outflare", path: "sdk/swift/Sources/Outflare"),
        .testTarget(name: "OutflareTests", dependencies: ["Outflare"], path: "sdk/swift/Tests/OutflareTests"),
    ]
)
