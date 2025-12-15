package main

import (
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// --- MODELOS ---

// Ofertas (Para el mapa)
type OfferResponse struct {
	ID          uint    `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Price       float64 `json:"price"`
	Category    string  `json:"category"`
	Status      string  `json:"status"` // <--- NUEVO CAMPO: 'active', 'flash', 'suspended'
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	Distance    float64 `json:"distance_meters"`
}

// Vehículos (Para el usuario)
type Vehicle struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	UserID   string `gorm:"index" json:"user_id"`
	Type     string `json:"type"` // 'car' o 'moto'
	Brand    string `json:"brand"`
	Model    string `json:"model"`
	Year     int    `json:"year"`
	IsActive bool   `gorm:"default:true" json:"is_active"`
}

// Wallet (Billetera del usuario)
type Wallet struct {
	UserID         string  `gorm:"primaryKey" json:"user_id"`
	Balance        float64 `json:"balance"`
	LifetimePoints float64 `json:"lifetime_points"`
	Goal           float64 `gorm:"default:500" json:"goal"`
	Status         string  `gorm:"default:'active'" json:"status"` // 'active', 'pending', 'frozen'
	LevelName      string  `gorm:"default:'Novato'" json:"level_name"`
}

var db *gorm.DB

func main() {
	_ = godotenv.Load()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("❌ Error: DATABASE_URL no configurada")
	}

	var err error
	db, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Fatal("❌ Error DB:", err)
	}

	// Migración automática (Crea tablas si no existen, útil como respaldo)
	db.AutoMigrate(&Vehicle{}, &Wallet{})

	r := gin.Default()

	// CORS (Permitir acceso desde la App)
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	r.GET("/", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "online ⚡"})
	})

	// --- RUTAS ---
	r.GET("/api/offers", getNearbyOffers)            // Buscar ofertas
	r.POST("/api/vehicles", createVehicle)           // Guardar vehículo
	r.GET("/api/vehicles/:user_id", getUserVehicles) // Consultar vehículos
	// Wallet
	r.GET("/api/wallet/:user_id", getWallet)
	r.POST("/api/wallet/redeem", requestRedeem)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}

// --- CONTROLADORES ---

func createVehicle(c *gin.Context) {
	var v Vehicle
	if err := c.ShouldBindJSON(&v); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Guardar en DB
	result := db.Create(&v)
	if result.Error != nil {
		c.JSON(500, gin.H{"error": "Error guardando vehículo"})
		return
	}
	c.JSON(201, v)
}

func getUserVehicles(c *gin.Context) {
	userID := c.Param("user_id")
	var vehicles []Vehicle
	db.Where("user_id = ?", userID).Find(&vehicles)
	c.JSON(200, vehicles)
}

func getNearbyOffers(c *gin.Context) {
	latStr := c.Query("lat")
	lngStr := c.Query("lng")
	radiusStr := c.Query("radius")

	if latStr == "" || lngStr == "" {
		c.JSON(400, gin.H{"error": "Faltan lat/lng"})
		return
	}

	lat, _ := strconv.ParseFloat(latStr, 64)
	lng, _ := strconv.ParseFloat(lngStr, 64)
	radius, _ := strconv.ParseFloat(radiusStr, 64)
	if radius == 0 {
		radius = 5000
	}

	var offers []OfferResponse
	// Consulta Geoespacial
	// Seleccionamos 'status' para que el frontend decida el color del pin (Amarillo/Rojo/Gris)
	query := `
		SELECT 
            id, title, description, price, category, status,
		    ST_Y(location::geometry) as latitude, 
            ST_X(location::geometry) as longitude,
		    ST_Distance(location, ST_MakePoint(?, ?)::geography) as distance_meters
		FROM offers
		WHERE ST_DWithin(location, ST_MakePoint(?, ?)::geography, ?)
		ORDER BY distance_meters ASC LIMIT 50;`

	db.Raw(query, lng, lat, lng, lat, radius).Scan(&offers)
	c.JSON(200, offers)
}

func getWallet(c *gin.Context) {
	userID := c.Param("user_id")
	var wallet Wallet

	// Buscar billetera, si no existe, crearla
	if result := db.First(&wallet, "user_id = ?", userID); result.Error != nil {
		wallet = Wallet{
			UserID:         userID,
			Balance:        0,
			LifetimePoints: 0,
			Goal:           500,
			Status:         "active",
			LevelName:      "Novato",
		}
		db.Create(&wallet)
	}
	c.JSON(200, wallet)
}

func requestRedeem(c *gin.Context) {
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Falta user_id"})
		return
	}

	var wallet Wallet
	if err := db.First(&wallet, "user_id = ?", req.UserID).Error; err != nil {
		c.JSON(404, gin.H{"error": "Wallet no encontrada"})
		return
	}

	if wallet.Balance < wallet.Goal {
		c.JSON(400, gin.H{"error": "Saldo insuficiente"})
		return
	}

	// Actualizar estado
	wallet.Status = "pending"
	db.Save(&wallet)

	c.JSON(200, gin.H{"message": "Solicitud recibida", "new_status": "pending"})
}
