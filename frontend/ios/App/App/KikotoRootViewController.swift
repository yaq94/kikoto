import UIKit

/// The window's root. It hosts the bridge inside the privacy shield's capture
/// canvas, so the screen capture choice leaves the page out of screenshots.
/// The bridge keeps deciding the status bar and the supported orientations.
final class KikotoRootViewController: UIViewController {
    private let bridgeController = KikotoBridgeViewController()

    override func viewDidLoad() {
        super.viewDidLoad()
        // What a screenshot shows in place of the page.
        view.backgroundColor = .systemBackground
        var host: UIView = view
        if let canvas = KikotoPrivacyShield.shared.pageCanvas {
            fill(view, with: canvas)
            host = canvas
        }
        addChild(bridgeController)
        fill(host, with: bridgeController.view)
        bridgeController.didMove(toParent: self)
    }

    private func fill(_ container: UIView, with content: UIView) {
        content.translatesAutoresizingMaskIntoConstraints = false
        container.addSubview(content)
        NSLayoutConstraint.activate([
            content.topAnchor.constraint(equalTo: container.topAnchor),
            content.bottomAnchor.constraint(equalTo: container.bottomAnchor),
            content.leadingAnchor.constraint(equalTo: container.leadingAnchor),
            content.trailingAnchor.constraint(equalTo: container.trailingAnchor),
        ])
    }

    override var childForStatusBarStyle: UIViewController? { bridgeController }

    override var childForStatusBarHidden: UIViewController? { bridgeController }

    override var preferredStatusBarUpdateAnimation: UIStatusBarAnimation {
        bridgeController.preferredStatusBarUpdateAnimation
    }

    override var supportedInterfaceOrientations: UIInterfaceOrientationMask {
        // The bridge reads its orientations from the app's Info.plist as its view loads.
        isViewLoaded ? bridgeController.supportedInterfaceOrientations : super.supportedInterfaceOrientations
    }
}
