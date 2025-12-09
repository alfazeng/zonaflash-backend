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
	db.AutoMigrate(&Vehicle{})

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
	// Consulta Geoespacial ACTUALIZADA
	// Ahora seleccionamos también el campo 'status'
	query := `
		SELECT 
            id, title, description, price, category, status,
		    ST_Y(location::geometry) as latitude, 
            ST_X(location::geometry) as longitude,
		    ST_Distance(location, ST_MakePoint(?, ?)::geography) as distance_meters
		FROM offers
		WHERE ST_DWithin(location, ST_MakePoint(?, ?)::geography, ?)
		-- Nota: Ya no filtramos por "is_active = TRUE" aquí, porque queremos traer
        -- incluso las suspendidas (status='suspended') para mostrarlas en GRIS en el mapa.
        -- El filtro visual lo hace el Frontend.
		ORDER BY distance_meters ASC LIMIT 50;`

	db.Raw(query, lng, lat, lng, lat, radius).Scan(&offers)
	c.JSON(200, offers)
}
