package main

import "github.com/onprs/OCS-Tiku/internal/engine"

type ImageProcessor = engine.ImageProcessor
type ImageInfo = engine.ImageInfo
type captionTask = engine.CaptionTask

func newImageProcessor(dir string) (*ImageProcessor, error) {
	return engine.NewImageProcessor(dir)
}

var validateImageURL = engine.ValidateImageURL

var extractImageReferences = engine.ExtractImageReferences
