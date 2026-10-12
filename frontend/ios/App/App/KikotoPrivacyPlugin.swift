import AVFoundation
import Capacitor
import UIKit

/// Device privacy choices, shared with the Android shell under the same names.
/// They live in native defaults so the covers apply without a round trip to
/// the web view. iOS has no app lock, so those fields always read off.
struct KikotoPrivacySettings {
    static let lockScreenContents = ["full", "hideCover", "hidden"]

    private static let recentsShieldKey = "kikoto.privacy.recentsShield"
    private static let lockScreenContentKey = "kikoto.privacy.lockScreenContent"
    private static let speakerConfirmKey = "kikoto.privacy.speakerConfirm"
    private static let screenSecureKey = "kikoto.privacy.screenSecure"

    /// Blurs the app in the app switcher and under system screens.
    var recentsShield: Bool
    /// What the page's Media Session gives the lock screen and Control Center.
    var lockScreenContent: String
    /// Playback through the device speaker waits for a confirmation in the page.
    var speakerConfirm: Bool
    /// Leaves the page out of screenshots, and covers the app and closes
    /// Picture-in-Picture lyrics while the screen is captured.
    var screenSecure: Bool

    static func read() -> KikotoPrivacySettings {
        let defaults = UserDefaults.standard
        let content = defaults.string(forKey: lockScreenContentKey)
        return KikotoPrivacySettings(
            recentsShield: defaults.object(forKey: recentsShieldKey) as? Bool ?? true,
            lockScreenContent: content.flatMap { lockScreenContents.contains($0) ? $0 : nil } ?? "hideCover",
            speakerConfirm: defaults.object(forKey: speakerConfirmKey) as? Bool ?? true,
            screenSecure: defaults.object(forKey: screenSecureKey) as? Bool ?? false
        )
    }

    func store() {
        let defaults = UserDefaults.standard
        defaults.set(recentsShield, forKey: Self.recentsShieldKey)
        defaults.set(lockScreenContent, forKey: Self.lockScreenContentKey)
        defaults.set(speakerConfirm, forKey: Self.speakerConfirmKey)
        defaults.set(screenSecure, forKey: Self.screenSecureKey)
    }

    var result: [String: Any] {
        [
            "recentsShield": recentsShield,
            "lockScreenContent": lockScreenContent,
            "speakerConfirm": speakerConfirm,
            "screenSecure": screenSecure,
            "appLock": false,
            "appLockTimeoutSeconds": 0,
        ]
    }
}

/// A view the system leaves out of screenshots, screen recordings, and
/// mirroring while `isSecure` is on. UIKit has no public way to exclude a view
/// from capture; the system does it for the text canvas of a secure text
/// field, together with everything placed inside that canvas.
@MainActor
final class KikotoCaptureCanvas {
    private let field = UITextField()
    /// Nil when this system's text field has no canvas to host a view in.
    let view: UIView?

    init() {
        view = field.subviews.first { String(describing: type(of: $0)).contains("CanvasView") }
        view?.isUserInteractionEnabled = true
    }

    var isSecure: Bool {
        get { field.isSecureTextEntry }
        set { field.isSecureTextEntry = newValue }
    }
}

/// Covers the app with a blur while its scene is inactive, so the app switcher
/// snapshot and the switch gesture do not show the page, and while the screen
/// is recorded, mirrored, or otherwise captured. A screenshot gives no warning
/// to cover the page in time, so the page lives in a capture canvas that the
/// screen capture choice turns secure. The choices come from
/// `KikotoPrivacySettings`: the protection must be in place when the scene
/// resigns active or a capture begins, without a round trip to the web view.
@MainActor
final class KikotoPrivacyShield {
    static let shared = KikotoPrivacyShield()
    /// Posted on the main queue when a screen capture begins while its cover is enabled.
    nonisolated static let screenCaptureDidBegin = Notification.Name("KikotoScreenCaptureDidBegin")

    private let canvas = KikotoCaptureCanvas()
    private weak var scene: UIWindowScene?
    private var captureObservation: Any?
    private var coverWindow: UIWindow?
    private var captureIcon: UIView?
    private var inactive = false
    private var captured = false
    private var settings = KikotoPrivacySettings.read()

    /// Where the page belongs so the screen capture choice keeps it out of
    /// screenshots; nil when this system has no such view.
    var pageCanvas: UIView? { canvas.view }

    /// Applies stored settings; turning on the capture cover during a capture also closes screen lyrics.
    func settingsChanged(_ next: KikotoPrivacySettings) {
        let coverAdded = next.screenSecure && !settings.screenSecure
        settings = next
        refresh()
        if coverAdded && captured { NotificationCenter.default.post(name: Self.screenCaptureDidBegin, object: nil) }
    }

    /// Starts following the scene's capture state.
    func attach(_ scene: UIWindowScene) {
        guard self.scene !== scene else { return }
        self.scene = scene
        if #available(iOS 17.0, *) {
            captureObservation = scene.registerForTraitChanges([UITraitSceneCaptureState.self]) {
                [weak self] (scene: UIWindowScene, _: UITraitCollection) in
                self?.setCaptured(scene.traitCollection.sceneCaptureState == .active)
            }
            setCaptured(scene.traitCollection.sceneCaptureState == .active)
        } else {
            let screen = scene.screen
            captureObservation = NotificationCenter.default.addObserver(
                forName: UIScreen.capturedDidChangeNotification,
                object: screen,
                queue: .main
            ) { [weak self] _ in
                MainActor.assumeIsolated { self?.setCaptured(screen.isCaptured) }
            }
            setCaptured(screen.isCaptured)
        }
    }

    func setInactive(_ inactive: Bool) {
        self.inactive = inactive
        refresh()
    }

    private func setCaptured(_ captured: Bool) {
        let began = captured && !self.captured
        self.captured = captured
        refresh()
        if began && settings.screenSecure {
            NotificationCenter.default.post(name: Self.screenCaptureDidBegin, object: nil)
        }
    }

    private func refresh() {
        canvas.isSecure = settings.screenSecure
        let forCapture = captured && settings.screenSecure
        guard forCapture || (inactive && settings.recentsShield), let scene else {
            coverWindow?.isHidden = true
            coverWindow = nil
            captureIcon = nil
            return
        }
        if coverWindow == nil { coverWindow = makeCover(in: scene) }
        // The capture cover stays while the app is in use, so it says why the page is hidden.
        captureIcon?.isHidden = !forCapture
    }

    private func makeCover(in scene: UIWindowScene) -> UIWindow {
        // A separate window above alerts also covers presented sheets and pickers.
        let window = UIWindow(windowScene: scene)
        window.windowLevel = .alert + 1
        window.backgroundColor = .clear
        let controller = UIViewController()
        controller.view.backgroundColor = .clear
        let blur = UIVisualEffectView(effect: UIBlurEffect(style: .systemThickMaterial))
        blur.frame = controller.view.bounds
        blur.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        controller.view.addSubview(blur)
        let icon = UIImageView(image: UIImage(systemName: "eye.slash"))
        icon.tintColor = .secondaryLabel
        icon.preferredSymbolConfiguration = UIImage.SymbolConfiguration(pointSize: 44, weight: .regular)
        icon.translatesAutoresizingMaskIntoConstraints = false
        blur.contentView.addSubview(icon)
        NSLayoutConstraint.activate([
            icon.centerXAnchor.constraint(equalTo: blur.contentView.centerXAnchor),
            icon.centerYAnchor.constraint(equalTo: blur.contentView.centerYAnchor),
        ])
        captureIcon = icon
        window.rootViewController = controller
        window.isHidden = false
        return window
    }
}

/// Stores the device privacy settings for Settings and reports the audio
/// output: whether playback would use the device's own speaker, and when the
/// output in use disappears, such as headphones being unplugged or a Bluetooth
/// device disconnecting. On that loss the page pauses at once, so playback
/// does not move to the speaker. Reconnecting never resumes playback.
@objc(KikotoPrivacyPlugin)
public class KikotoPrivacyPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "KikotoPrivacyPlugin"
    public let jsName = "KikotoPrivacy"
    public let pluginMethods: [CAPPluginMethod] = [
        CAPPluginMethod(name: "getSettings", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "setSettings", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "status", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "setUnlockLabels", returnType: CAPPluginReturnPromise),
        CAPPluginMethod(name: "outputStatus", returnType: CAPPluginReturnPromise),
    ]

    /// The last reported output, so a route change that keeps the speaker reports nothing.
    private var phoneSpeaker = true

    override public func load() {
        phoneSpeaker = Self.outputIsSpeaker()
        NotificationCenter.default.addObserver(
            self,
            selector: #selector(audioRouteChanged(_:)),
            name: AVAudioSession.routeChangeNotification,
            object: nil
        )
    }

    deinit {
        NotificationCenter.default.removeObserver(self)
    }

    @objc func getSettings(_ call: CAPPluginCall) {
        call.resolve(KikotoPrivacySettings.read().result)
    }

    /// Stores the provided fields; a missing or unknown value keeps the current choice.
    @objc func setSettings(_ call: CAPPluginCall) {
        let recentsShield = call.getBool("recentsShield")
        let lockScreenContent = call.getString("lockScreenContent")
        let speakerConfirm = call.getBool("speakerConfirm")
        let screenSecure = call.getBool("screenSecure")
        DispatchQueue.main.async {
            var settings = KikotoPrivacySettings.read()
            if let recentsShield { settings.recentsShield = recentsShield }
            if let lockScreenContent, KikotoPrivacySettings.lockScreenContents.contains(lockScreenContent) {
                settings.lockScreenContent = lockScreenContent
            }
            if let speakerConfirm { settings.speakerConfirm = speakerConfirm }
            if let screenSecure { settings.screenSecure = screenSecure }
            settings.store()
            KikotoPrivacyShield.shared.settingsChanged(settings)
            call.resolve(settings.result)
        }
    }

    /// The app lock is an Android shell feature. The screen capture choice
    /// blanks screenshots only where the page sits in a capture canvas.
    @objc func status(_ call: CAPPluginCall) {
        DispatchQueue.main.async {
            call.resolve([
                "appLockSupported": false,
                "appLockAvailable": false,
                "screenshotsHidden": KikotoPrivacyShield.shared.pageCanvas != nil,
            ])
        }
    }

    /// Part of the shared interface; iOS has no app lock prompt to label.
    @objc func setUnlockLabels(_ call: CAPPluginCall) {
        call.resolve()
    }

    @objc func outputStatus(_ call: CAPPluginCall) {
        call.resolve(["phoneSpeaker": Self.outputIsSpeaker()])
    }

    /// Whether audio would play through the device's own speaker.
    private static func outputIsSpeaker() -> Bool {
        let outputs = AVAudioSession.sharedInstance().currentRoute.outputs
        return outputs.contains { $0.portType == .builtInSpeaker || $0.portType == .builtInReceiver }
    }

    @objc private func audioRouteChanged(_ notification: Notification) {
        let rawReason = notification.userInfo?[AVAudioSessionRouteChangeReasonKey] as? UInt
        let lost = rawReason.flatMap(AVAudioSession.RouteChangeReason.init(rawValue:)) == .oldDeviceUnavailable
        let speaker = Self.outputIsSpeaker()
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            if lost {
                // Stop the page media at once, also while the web player is still
                // loading a source and would otherwise ignore an element pause.
                self.bridge?.webView?.evaluateJavaScript(
                    "document.querySelectorAll('audio,video').forEach(function(media){media.pause();});"
                )
                self.notifyListeners("outputLost", data: [:])
            }
            guard speaker != self.phoneSpeaker else { return }
            self.phoneSpeaker = speaker
            self.notifyListeners("outputChanged", data: ["phoneSpeaker": speaker])
        }
    }
}
