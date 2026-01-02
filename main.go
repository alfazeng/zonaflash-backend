package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// --- MODELOS ---

// Ofertas (Para el mapa)
type OfferResponse struct {
	ID          string  `json:"id"`
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
	ID       string `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
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

// Location (Puntos cazados)
type Location struct {
	ID               string      `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	UserID           string      `gorm:"index" json:"user_id"`
	ShopName         string      `json:"shop_name"`
	Category         string      `json:"category"`
	PhotoURL         string      `json:"photo_url"`
	Latitude         float64     `json:"latitude"`
	Longitude        float64     `json:"longitude"`
	Status           string      `gorm:"default:'pending'" json:"status"` // 'pending', 'approved', 'rejected'
	IsShadow         bool        `json:"is_shadow"`
	ActivationStatus string      `json:"activation_status"`
	AssetType        string      `json:"asset_type"`
	Geom             interface{} `gorm:"type:geography(POINT,4326)" json:"-"`
}

// Transaction (Historial de puntos)
type Transaction struct {
	ID          string    `gorm:"primaryKey;type:uuid;default:gen_random_uuid()" json:"id"`
	UserID      string    `gorm:"index" json:"user_id"`
	Type        string    `json:"type"` // 'earning'
	Amount      float64   `json:"amount"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
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

	// 0. Asegurar PostGIS
	db.Exec("CREATE EXTENSION IF NOT EXISTS postgis;")

	// Migración automática (Crea tablas si no existen, útil como respaldo)
	db.AutoMigrate(&Vehicle{}, &Wallet{}, &Location{}, &Transaction{})

	// Fix: Eliminar constraint de user_id si existe (MVP mode)
	db.Exec("ALTER TABLE wallets DROP CONSTRAINT IF EXISTS wallets_user_id_fkey;")

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
	// Wallet
	r.GET("/api/wallet/:user_id", getWallet)
	r.POST("/api/wallet/redeem", requestRedeem)
	// Hunter
	r.POST("/api/hunter/submit", submitHuntHandler)

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
	// Consulta Geoespacial (UNION ALL entre Ofertas y Puntos Cazados)
	query := `
		(
			SELECT 
				id::text, 
				title, 
				description, 
				price, 
				category::text, -- Cast para el Enum de categoría
				status::text,   -- Cast para el Enum de status (ESTE ES EL VITAL)
				ST_Y(location::geometry) as latitude, 
				ST_X(location::geometry) as longitude,
				ST_Distance(location, ST_MakePoint(?, ?)::geography) as distance_meters
			FROM offers
			WHERE ST_DWithin(location, ST_MakePoint(?, ?)::geography, ?)
		)
		UNION ALL
		(
			SELECT 
				id::text, 
				shop_name as title, 
				'' as description, 
				0 as price, 
				category,        -- Ya es texto
				CASE 
					WHEN category IN ('station_moto', 'station_car') THEN 'shadow'
					ELSE status 
				END as status,   -- Aquí ya es texto
				latitude, 
				longitude,
				ST_Distance(geom, ST_MakePoint(?, ?)::geography) as distance_meters
			FROM locations
			WHERE (is_shadow = true OR status = 'approved')
			AND ST_DWithin(geom, ST_MakePoint(?, ?)::geography, ?)
		)
		ORDER BY distance_meters ASC LIMIT 50;`

	db.Raw(query, lng, lat, lng, lat, radius, lng, lat, lng, lat, radius).Scan(&offers)
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

func submitHuntHandler(c *gin.Context) {
	var req struct {
		UserID           string  `json:"user_id"`
		ShopName         string  `json:"shop_name"`
		PhotoURL         string  `json:"photo_url"`
		Category         string  `json:"category"`
		Latitude         float64 `json:"latitude"`
		Longitude        float64 `json:"longitude"`
		IsShadow         bool    `json:"is_shadow"`
		ActivationStatus string  `json:"activation_status"`
		AssetType        string  `json:"asset_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}

	// 0. PROXIMITY CHECK (Anti-Duplicado) - 20 metros
	var count int64
	checkQuery := `
		SELECT count(*) 
		FROM locations 
		WHERE category = ? 
		AND ST_DWithin(geom, ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography, 20)`
	db.Raw(checkQuery, req.Category, req.Longitude, req.Latitude).Scan(&count)

	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Este punto ya ha sido capturado recientemente"})
		return
	}

	tx := db.Begin()

	// 1. Insert Location
	loc := Location{
		UserID:           req.UserID,
		ShopName:         req.ShopName,
		Category:         req.Category,
		PhotoURL:         req.PhotoURL,
		Latitude:         req.Latitude,
		Longitude:        req.Longitude,
		Status:           "pending",
		IsShadow:         req.IsShadow,
		ActivationStatus: req.ActivationStatus,
		AssetType:        req.AssetType,
	}

	// 1.1 Poblar Geom manualmente para PostGIS
	if err := tx.Create(&loc).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error creating location"})
		return
	}

	updateGeom := "UPDATE locations SET geom = ST_SetSRID(ST_MakePoint(?, ?), 4326)::geography WHERE id = ?"
	if err := tx.Exec(updateGeom, req.Longitude, req.Latitude, loc.ID).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error updating geography"})
		return
	}

	// 2. Insert Transaction
	trans := Transaction{
		UserID:      req.UserID,
		Type:        "earning",
		Amount:      10,
		Description: "Caza: " + req.ShopName,
		CreatedAt:   time.Now(),
	}
	if err := tx.Create(&trans).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error creating transaction"})
		return
	}

	// 3. Upsert Wallet
	// Raw SQL para asegurar que el constraint no bloquee (MVP)
	tx.Exec("SET CONSTRAINTS ALL DEFERRED")

	// Usamos raw SQL para upsert atómico y sencillo
	if err := tx.Exec(`
        INSERT INTO wallets (user_id, balance, lifetime_points, goal, status, level_name) 
        VALUES (?, 10, 10, 500, 'active', 'Novato') 
        ON CONFLICT (user_id) 
        DO UPDATE SET balance = wallets.balance + 10, lifetime_points = wallets.lifetime_points + 10
    `, req.UserID).Error; err != nil {
		tx.Rollback()
		c.JSON(500, gin.H{"error": "Error updating wallet"})
		return
	}

	tx.Commit()
	c.JSON(200, gin.H{"message": "Hunt submitted successfully", "points": 10})
}
