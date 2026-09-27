// Fails when a simulator screenshot is (nearly) one flat colour — the
// blank white page a WKWebView shows when it never loaded the till
// (ut-docs#3068) — and prints a small JPEG preview as base64 in a
// collapsed log group, so a reviewer who can't download the artifact can
// still look at it. Usage: swift ios-screenshot-check.swift <png>
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let path = CommandLine.arguments[1]
guard let src = CGImageSourceCreateWithURL(URL(fileURLWithPath: path) as CFURL, nil),
      let image = CGImageSourceCreateImageAtIndex(src, 0, nil) else {
    print("::error::cannot read \(path)"); exit(1)
}

// Draw into a known RGBA layout and count distinct colours on a sample grid.
let w = image.width, h = image.height
var pixels = [UInt8](repeating: 0, count: w * h * 4)
let ctx = CGContext(data: &pixels, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                    space: CGColorSpaceCreateDeviceRGB(),
                    bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
ctx.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
var colours = Set<UInt32>()
var nonBackground = 0
let bg = Array(pixels[0..<4])
for y in stride(from: 0, to: h, by: 8) {
    for x in stride(from: 0, to: w, by: 8) {
        let i = (y * w + x) * 4
        let p = Array(pixels[i..<i + 4])
        colours.insert(UInt32(p[0]) << 16 | UInt32(p[1]) << 8 | UInt32(p[2]))
        if p != bg { nonBackground += 1 }
    }
}
let samples = ((h + 7) / 8) * ((w + 7) / 8)
let share = Double(nonBackground) / Double(samples)
print("\(path): \(w)x\(h), \(colours.count) distinct colours, \(Int(share * 100))% non-background")

// Preview: 240px wide JPEG.
let pw = 240, ph = h * pw / w
let small = CGContext(data: nil, width: pw, height: ph, bitsPerComponent: 8, bytesPerRow: 0,
                      space: CGColorSpaceCreateDeviceRGB(),
                      bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
small.interpolationQuality = .medium
small.draw(image, in: CGRect(x: 0, y: 0, width: pw, height: ph))
let data = NSMutableData()
if let thumb = small.makeImage(),
   let dest = CGImageDestinationCreateWithData(data, UTType.jpeg.identifier as CFString, 1, nil) {
    CGImageDestinationAddImage(dest, thumb, [kCGImageDestinationLossyCompressionQuality: 0.6] as CFDictionary)
    CGImageDestinationFinalize(dest)
    print("::group::preview \(path) (base64 JPEG)")
    print((data as Data).base64EncodedString())
    print("::endgroup::")
}

// A rendered till page has text, buttons and tiles; a blank page or a
// bare spinner is a flat field with a few pixels of something.
if colours.count < 16 || share < 0.02 {
    print("::error::\(path) looks blank — the WebView probably never showed the till")
    exit(1)
}
