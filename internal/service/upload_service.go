package service

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dujiao-next/internal/config"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"github.com/google/uuid"
)

var allowedUploadScenes = map[string]struct{}{
	"product":  {},
	"post":     {},
	"banner":   {},
	"editor":   {},
	"common":   {},
	"category": {},
	"telegram": {},
	"reseller": {},
}

// UploadService 文件上传服务
type UploadService struct {
	cfg *config.Config
}

// UploadValidationError 表示上传内容不符合业务校验规则，可直接展示给管理员。
type UploadValidationError struct {
	Message string
}

func (e *UploadValidationError) Error() string {
	return e.Message
}

func newUploadValidationError(format string, args ...interface{}) error {
	return &UploadValidationError{Message: fmt.Sprintf(format, args...)}
}

func IsUploadValidationError(err error) bool {
	var validationErr *UploadValidationError
	return errors.As(err, &validationErr)
}

// NewUploadService 创建文件上传服务实例
func NewUploadService(cfg *config.Config) *UploadService {
	return &UploadService{cfg: cfg}
}

// UploadResult 上传结果（包含完整元数据）
type UploadResult struct {
	URL      string // 相对路径
	Filename string // 原始文件名
	MimeType string
	Size     int64
	Width    int
	Height   int
}

// SaveFile 保存上传的文件（保留原签名兼容性）
func (s *UploadService) SaveFile(file *multipart.FileHeader, scene string) (string, error) {
	result, err := s.SaveFileWithMeta(file, scene)
	if err != nil {
		return "", err
	}
	return result.URL, nil
}

// SaveFileWithMeta 保存上传的文件并返回完整元数据
func (s *UploadService) SaveFileWithMeta(file *multipart.FileHeader, scene string) (*UploadResult, error) {
	normalizedScene := normalizeUploadScene(scene)

	// 验证文件大小
	if file.Size > s.cfg.Upload.MaxSize {
		return nil, newUploadValidationError("文件大小超过限制（最大 %d MB）", s.cfg.Upload.MaxSize/1024/1024)
	}

	// 获取文件扩展名
	ext := strings.ToLower(filepath.Ext(file.Filename))
	if normalizedScene != "telegram" && len(s.cfg.Upload.AllowedExtensions) > 0 {
		if ext == "" || !isAllowedExtension(ext, s.cfg.Upload.AllowedExtensions) {
			return nil, newUploadValidationError("文件扩展名不被允许: %s", ext)
		}
	}

	// 验证文件类型
	src, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()

	// 读取文件头部识别 MIME 类型
	buffer := make([]byte, 512)
	_, err = src.Read(buffer)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if _, err := src.Seek(0, 0); err != nil { // 重置文件读取位置
		return nil, err
	}

	contentType := http.DetectContentType(buffer)
	// http.DetectContentType 无法识别 SVG，需根据扩展名和内容特征补充判断
	if ext == ".svg" && isSVGContent(buffer) {
		contentType = "image/svg+xml"
	}
	if normalizedScene != "telegram" && len(s.cfg.Upload.AllowedTypes) > 0 {
		allowed := false
		for _, t := range s.cfg.Upload.AllowedTypes {
			if strings.EqualFold(contentType, t) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, newUploadValidationError("文件类型不被允许: %s", contentType)
		}
	}

	var imgWidth, imgHeight int
	if strings.HasPrefix(contentType, "image/") && contentType != "image/svg+xml" {
		if _, err := src.Seek(0, 0); err != nil {
			return nil, err
		}
		width, height, err := decodeImageDimensions(src, contentType)
		if err != nil {
			return nil, newUploadValidationError("%s", err.Error())
		}
		imgWidth = width
		imgHeight = height
		if s.cfg.Upload.MaxWidth > 0 && width > s.cfg.Upload.MaxWidth {
			return nil, newUploadValidationError("图片宽度超过限制（最大 %d）", s.cfg.Upload.MaxWidth)
		}
		if s.cfg.Upload.MaxHeight > 0 && height > s.cfg.Upload.MaxHeight {
			return nil, newUploadValidationError("图片高度超过限制（最大 %d）", s.cfg.Upload.MaxHeight)
		}
	}

	// SVG 安全检查：禁止嵌入脚本和外部引用
	if contentType == "image/svg+xml" {
		if _, err := src.Seek(0, 0); err != nil {
			return nil, err
		}
		svgData, err := io.ReadAll(src)
		if err != nil {
			return nil, err
		}
		if err := validateSVGSafety(svgData); err != nil {
			return nil, newUploadValidationError("%s", err.Error())
		}
		if _, err := src.Seek(0, 0); err != nil {
			return nil, err
		}
	}

	if _, err := src.Seek(0, 0); err != nil {
		return nil, err
	}
	// 生成唯一文件名
	filename := fmt.Sprintf("%s%s", uuid.New().String(), ext)
	now := time.Now()
	year := now.Format("2006")
	month := now.Format("01")
	savePath := filepath.Join("uploads", normalizedScene, year, month, filename)

	// 确保上传目录存在
	if err := os.MkdirAll(filepath.Dir(savePath), 0755); err != nil {
		return nil, err
	}

	// 保存文件
	dst, err := os.Create(savePath)
	if err != nil {
		return nil, err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)
	if err != nil {
		return nil, err
	}

	return &UploadResult{
		URL:      fmt.Sprintf("/uploads/%s/%s/%s/%s", normalizedScene, year, month, filename),
		Filename: file.Filename,
		MimeType: contentType,
		Size:     file.Size,
		Width:    imgWidth,
		Height:   imgHeight,
	}, nil
}

func normalizeUploadScene(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "common"
	}
	if _, ok := allowedUploadScenes[value]; ok {
		return value
	}
	return "common"
}

func isAllowedExtension(ext string, allowed []string) bool {
	for _, allowedExt := range allowed {
		normalized := strings.ToLower(strings.TrimSpace(allowedExt))
		if normalized == "" {
			continue
		}
		if !strings.HasPrefix(normalized, ".") {
			normalized = "." + normalized
		}
		if strings.EqualFold(ext, normalized) {
			return true
		}
	}
	return false
}

func decodeImageDimensions(src io.ReadSeeker, contentType string) (int, int, error) {
	if strings.EqualFold(contentType, "image/webp") {
		width, height, err := decodeWebPDimensions(src)
		if err != nil {
			return 0, 0, fmt.Errorf("无法解析 WebP 图片: %w", err)
		}
		return width, height, nil
	}

	if _, err := src.Seek(0, 0); err != nil {
		return 0, 0, err
	}
	cfg, _, err := image.DecodeConfig(src)
	if err != nil {
		return 0, 0, fmt.Errorf("无法解析图片: %w", err)
	}
	return cfg.Width, cfg.Height, nil
}

// isSVGContent 通过文件内容判断是否为 SVG
func isSVGContent(buf []byte) bool {
	content := strings.TrimSpace(string(buf))
	// SVG 文件通常以 XML 声明或 <svg 标签开头
	return strings.HasPrefix(content, "<?xml") ||
		strings.HasPrefix(content, "<svg") ||
		strings.Contains(content, "<svg")
}

// validateSVGSafety 检查 SVG 内容安全性，禁止脚本和危险元素
func validateSVGSafety(data []byte) error {
	content := strings.ToLower(string(data))
	// 禁止脚本标签
	if strings.Contains(content, "<script") {
		return fmt.Errorf("SVG 文件不允许包含 <script> 标签")
	}
	// 禁止事件处理属性（onclick, onload, onerror 等）
	dangerousAttrs := []string{
		"onload", "onclick", "onerror", "onmouseover", "onmouseout",
		"onmousemove", "onfocus", "onblur", "onchange", "onsubmit",
		"onanimationstart", "onanimationend", "onanimationiteration",
	}
	for _, attr := range dangerousAttrs {
		if strings.Contains(content, attr+"=") || strings.Contains(content, attr+" =") {
			return fmt.Errorf("SVG 文件不允许包含事件处理属性: %s", attr)
		}
	}
	// 禁止 javascript: 协议
	if strings.Contains(content, "javascript:") {
		return fmt.Errorf("SVG 文件不允许包含 javascript: 协议")
	}
	// 禁止 data: URI（可用于绕过 CSP）
	if strings.Contains(content, "data:text/html") || strings.Contains(content, "data:application") {
		return fmt.Errorf("SVG 文件不允许包含危险的 data: URI")
	}
	// 禁止 foreignObject（可嵌入 HTML）
	if strings.Contains(content, "<foreignobject") {
		return fmt.Errorf("SVG 文件不允许包含 <foreignObject> 元素")
	}
	return nil
}

func decodeWebPDimensions(src io.ReadSeeker) (int, int, error) {
	if _, err := src.Seek(0, 0); err != nil {
		return 0, 0, err
	}

	header := make([]byte, 12)
	if _, err := io.ReadFull(src, header); err != nil {
		return 0, 0, err
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WEBP" {
		return 0, 0, fmt.Errorf("无效的 WebP 文件头")
	}

	for {
		chunkHeader := make([]byte, 8)
		if _, err := io.ReadFull(src, chunkHeader); err != nil {
			return 0, 0, err
		}
		chunkType := string(chunkHeader[0:4])
		chunkSize := int(binary.LittleEndian.Uint32(chunkHeader[4:8]))
		if chunkSize < 0 {
			return 0, 0, fmt.Errorf("无效的 WebP chunk")
		}

		data := make([]byte, chunkSize)
		if _, err := io.ReadFull(src, data); err != nil {
			return 0, 0, err
		}

		if chunkType == "VP8X" {
			if len(data) < 10 {
				return 0, 0, fmt.Errorf("VP8X chunk 长度不足")
			}
			width := 1 + int(data[4]) + int(data[5])<<8 + int(data[6])<<16
			height := 1 + int(data[7]) + int(data[8])<<8 + int(data[9])<<16
			return width, height, nil
		}
		if chunkType == "VP8 " {
			if len(data) < 10 {
				return 0, 0, fmt.Errorf("VP8 chunk 长度不足")
			}
			width := int(binary.LittleEndian.Uint16(data[6:8]) & 0x3FFF)
			height := int(binary.LittleEndian.Uint16(data[8:10]) & 0x3FFF)
			return width, height, nil
		}
		if chunkType == "VP8L" {
			if len(data) < 5 {
				return 0, 0, fmt.Errorf("VP8L chunk 长度不足")
			}
			if data[0] != 0x2f {
				return 0, 0, fmt.Errorf("VP8L 签名无效")
			}
			bits := binary.LittleEndian.Uint32(data[1:5])
			width := int(bits&0x3FFF) + 1
			height := int((bits>>14)&0x3FFF) + 1
			return width, height, nil
		}

		if chunkSize%2 == 1 {
			if _, err := src.Seek(1, io.SeekCurrent); err != nil {
				return 0, 0, err
			}
		}
	}
}
